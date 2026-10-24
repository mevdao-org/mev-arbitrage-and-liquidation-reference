// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/**
 * @title FlashLoanArbAndLiquidationExecutor
 * @author MEV DAO (https://mevdao.org)
 * @notice Atomic Zero-Capital Aave V3 Flash-Loan Executor supporting:
 *         1. Cross-DEX Atomic Arbitrage (Uniswap V2 <-> Uniswap V3 / SushiSwap)
 *         2. Aave V3 Health-Factor < 1.0 Liquidations (`liquidationCall`)
 *         3. Dynamic `block.coinbase` Profit-Sharing Builder Bribe
 *
 *         Full Engineering Guides:
 *         - https://mevdao.org/blog/how-to-build-a-profitable-mev-arbitrage-bot
 *         - https://mevdao.org/blog/how-liquidation-bots-work-on-aave-and-why-they-re-hard-to-win
 */

interface IERC20 {
    function balanceOf(address account) external view returns (uint256);
    function transfer(address to, uint256 amount) external returns (bool);
    function approve(address spender, uint256 amount) external returns (bool);
}

interface IWETH is IERC20 {
    function withdraw(uint256 wad) external;
}

interface IAaveV3Pool {
    function flashLoanSimple(
        address receiverAddress,
        address asset,
        uint256 amount,
        bytes calldata params,
        uint16 referralCode
    ) external;

    function liquidationCall(
        address collateralAsset,
        address debtAsset,
        address user,
        uint256 debtToCover,
        bool receiveAToken
    ) external;
}

interface IUniswapV2Router {
    function swapExactTokensForTokens(
        uint256 amountIn,
        uint256 amountOutMin,
        address[] calldata path,
        address to,
        uint256 deadline
    ) external returns (uint256[] memory amounts);
}

interface ISwapRouterV3 {
    struct ExactInputSingleParams {
        address tokenIn;
        address tokenOut;
        uint24 fee;
        address recipient;
        uint256 deadline;
        uint256 amountIn;
        uint256 amountOutMinimum;
        uint160 sqrtPriceLimitX96;
    }

    function exactInputSingle(ExactInputSingleParams calldata params) external payable returns (uint256 amountOut);
}

contract FlashLoanArbAndLiquidationExecutor {
    address public immutable owner;
    IAaveV3Pool public immutable aavePool;
    IWETH public immutable weth;

    enum StrategyType {
        CROSS_DEX_ARBITRAGE,
        AAVE_V3_LIQUIDATION
    }

    struct ExecutionPayload {
        StrategyType strategy;
        address intermediateOrCollateralToken;
        address targetUser; // Used for Aave V3 liquidationCall
        address routerA;
        address routerB;
        uint24 uniV3Fee;
        uint256 minNetProfit;
        uint256 coinbaseBribeBps; // e.g., 8000 = 80% of net profit paid to block.coinbase
    }

    error Unauthorized();
    error InvalidFlashLoanInitiator();
    error UnprofitableExecution(uint256 grossOut, uint256 requiredMinimum);
    error BribeTransferFailed();

    constructor(address _aavePool, address _weth) {
        owner = msg.sender;
        aavePool = IAaveV3Pool(_aavePool);
        weth = IWETH(_weth);
    }

    modifier onlyOwner() {
        if (msg.sender != owner) revert Unauthorized();
        _;
    }

    /**
     * @notice Entry point called by the searcher bot inside a Flashbots bundle.
     */
    function executeFlashLoanStrategy(
        address borrowedAsset,
        uint256 flashAmount,
        ExecutionPayload calldata payload
    ) external onlyOwner {
        aavePool.flashLoanSimple(
            address(this),
            borrowedAsset,
            flashAmount,
            abi.encode(payload),
            0
        );
    }

    /**
     * @notice Callback invoked by Aave V3 Pool during `flashLoanSimple`.
     */
    function executeOperation(
        address asset,
        uint256 amount,
        uint256 premium,
        address initiator,
        bytes calldata params
    ) external returns (bool) {
        if (msg.sender != address(aavePool) || initiator != address(this)) {
            revert InvalidFlashLoanInitiator();
        }

        ExecutionPayload memory p = abi.decode(params, (ExecutionPayload));
        uint256 totalDebtOwed = amount + premium;

        if (p.strategy == StrategyType.CROSS_DEX_ARBITRAGE) {
            // Leg 1: Swap asset -> intermediateToken on Uniswap V3
            IERC20(asset).approve(p.routerA, amount);
            uint256 midAmount = ISwapRouterV3(p.routerA).exactInputSingle(
                ISwapRouterV3.ExactInputSingleParams({
                    tokenIn: asset,
                    tokenOut: p.intermediateOrCollateralToken,
                    fee: p.uniV3Fee,
                    recipient: address(this),
                    deadline: block.timestamp,
                    amountIn: amount,
                    amountOutMinimum: 1,
                    sqrtPriceLimitX96: 0
                })
            );

            // Leg 2: Swap intermediateToken -> asset on Uniswap V2 / SushiSwap
            IERC20(p.intermediateOrCollateralToken).approve(p.routerB, midAmount);
            address[] memory path = new address[](2);
            path[0] = p.intermediateOrCollateralToken;
            path[1] = asset;
            IUniswapV2Router(p.routerB).swapExactTokensForTokens(
                midAmount,
                totalDebtOwed + p.minNetProfit,
                path,
                address(this),
                block.timestamp
            );
        } else {
            // StrategyType.AAVE_V3_LIQUIDATION:
            // 1. Repay underwater borrower's debt via Aave V3 liquidationCall
            IERC20(asset).approve(address(aavePool), amount);
            aavePool.liquidationCall(
                p.intermediateOrCollateralToken,
                asset,
                p.targetUser,
                amount,
                false
            );

            // 2. Swap seized discounted collateral back into borrowed debt asset on Uniswap V3
            uint256 seizedCollateral = IERC20(p.intermediateOrCollateralToken).balanceOf(address(this));
            IERC20(p.intermediateOrCollateralToken).approve(p.routerA, seizedCollateral);
            ISwapRouterV3(p.routerA).exactInputSingle(
                ISwapRouterV3.ExactInputSingleParams({
                    tokenIn: p.intermediateOrCollateralToken,
                    tokenOut: asset,
                    fee: p.uniV3Fee,
                    recipient: address(this),
                    deadline: block.timestamp,
                    amountIn: seizedCollateral,
                    amountOutMinimum: totalDebtOwed + p.minNetProfit,
                    sqrtPriceLimitX96: 0
                })
            );
        }

        uint256 finalBalance = IERC20(asset).balanceOf(address(this));
        if (finalBalance < totalDebtOwed + p.minNetProfit) {
            revert UnprofitableExecution(finalBalance, totalDebtOwed + p.minNetProfit);
        }

        // Approve Aave V3 Pool to pull principal + flash loan fee (0.05%)
        IERC20(asset).approve(address(aavePool), totalDebtOwed);

        uint256 netProfit = finalBalance - totalDebtOwed;

        // If profit is in WETH, unwrap builder bribe share and pay block.coinbase directly
        if (asset == address(weth) && p.coinbaseBribeBps > 0) {
            uint256 bribeWei = (netProfit * p.coinbaseBribeBps) / 10_000;
            weth.withdraw(bribeWei);
            (bool ok, ) = block.coinbase.call{value: bribeWei}("");
            if (!ok) revert BribeTransferFailed();
        }

        // Transfer remaining net profit to searcher owner
        uint256 remainingProfit = IERC20(asset).balanceOf(address(this)) - totalDebtOwed;
        if (remainingProfit > 0) {
            IERC20(asset).transfer(owner, remainingProfit);
        }

        return true;
    }

    receive() external payable {}
}
