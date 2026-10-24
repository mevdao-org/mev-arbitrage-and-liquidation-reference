import "dotenv/config";
import {
  createPublicClient,
  formatEther,
  formatUnits,
  http,
  parseAbi,
  parseEther,
  type Address,
} from "viem";
import { mainnet } from "viem/chains";

/**
 * MEV DAO Atomic DEX Arbitrage & Aave V3 Health-Factor Monitor
 * Full Engineering Guides:
 * - https://mevdao.org/blog/how-to-build-a-profitable-mev-arbitrage-bot
 * - https://mevdao.org/blog/how-liquidation-bots-work-on-aave-and-why-they-re-hard-to-win
 * - https://mevdao.org/blog/how-mev-bots-make-money-on-ethereum-and-who-keeps-the-profit
 */

const UNISWAP_V2_PAIR_ABI = parseAbi([
  "function getReserves() external view returns (uint112 reserve0, uint112 reserve1, uint32 blockTimestampLast)",
]);

const AAVE_V3_POOL_ABI = parseAbi([
  "function getUserAccountData(address user) external view returns (uint256 totalCollateralBase, uint256 totalDebtBase, uint256 availableBorrowsBase, uint256 currentLiquidationThreshold, uint256 ltv, uint256 healthFactor)",
]);

const WETH_USDC_UNIV2 = "0xB4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc" as const;
const WETH_USDC_SUSHIV2 = "0x397FF1542f962076d0BFE58eA045FfA2d347ACa0" as const;
const AAVE_V3_POOL_MAINNET = "0x87870Bca3F3fD6335C3F4ce8392D69350B4fA4E2" as const;

/**
 * Exact Uniswap V2 constant-product output with 0.30% LP fee:
 * dy = (dx * 997 * y) / (x * 1000 + dx * 997)
 */
export function getUniswapV2AmountOut(
  amountIn: bigint,
  reserveIn: bigint,
  reserveOut: bigint
): bigint {
  if (amountIn <= 0n || reserveIn <= 0n || reserveOut <= 0n) return 0n;
  const amountInWithFee = amountIn * 997n;
  const numerator = amountInWithFee * reserveOut;
  const denominator = reserveIn * 1000n + amountInWithFee;
  return numerator / denominator;
}

/**
 * Evaluates a two-leg WETH -> USDC -> WETH arbitrage opportunity across two constant-product pools,
 * subtracting the 5 bps (0.05%) Aave V3 flash loan fee and builder bribe.
 */
export function evaluateTwoPoolFlashArbitrage(params: {
  flashLoanWethWei: bigint;
  poolA_ReserveUSDC: bigint;
  poolA_ReserveWETH: bigint;
  poolB_ReserveUSDC: bigint;
  poolB_ReserveWETH: bigint;
  aaveFlashFeeBps?: bigint;
}) {
  const flashFeeBps = params.aaveFlashFeeBps ?? 5n; // 0.05% Aave V3 flashLoanSimple fee
  const flashFeeWei = (params.flashLoanWethWei * flashFeeBps) / 10_000n;
  const totalRepaymentWei = params.flashLoanWethWei + flashFeeWei;

  // Leg 1: Swap WETH -> USDC on Pool A
  const usdcOut = getUniswapV2AmountOut(
    params.flashLoanWethWei,
    params.poolA_ReserveWETH,
    params.poolA_ReserveUSDC
  );

  // Leg 2: Swap USDC -> WETH on Pool B
  const wethBack = getUniswapV2AmountOut(
    usdcOut,
    params.poolB_ReserveUSDC,
    params.poolB_ReserveWETH
  );

  const netProfitBeforeGasWei = wethBack - totalRepaymentWei;
  return {
    flashLoanWethWei: params.flashLoanWethWei,
    usdcIntermediate: usdcOut,
    grossWethReturned: wethBack,
    totalRepaymentWei,
    netProfitBeforeGasWei,
    isProfitable: netProfitBeforeGasWei > 0n,
  };
}

/**
 * Checks an Aave V3 borrower's health factor (`healthFactor < 1e18` is eligible for liquidation).
 */
export async function checkAaveV3BorrowerHealth(rpcUrl: string, borrower: Address) {
  const client = createPublicClient({ chain: mainnet, transport: http(rpcUrl) });
  const data = await client.readContract({
    address: AAVE_V3_POOL_MAINNET,
    abi: AAVE_V3_POOL_ABI,
    functionName: "getUserAccountData",
    args: [borrower],
  });

  const healthFactor = data[5];
  return {
    borrower,
    totalCollateralUSD: formatUnits(data[0], 8),
    totalDebtUSD: formatUnits(data[1], 8),
    healthFactor: formatUnits(healthFactor, 18),
    isLiquidatable: healthFactor < 10n ** 18n,
  };
}

async function main() {
  const rpcUrl = process.env.ETH_HTTP_RPC_URL || "https://eth.llamarpc.com";
  const client = createPublicClient({ chain: mainnet, transport: http(rpcUrl) });

  console.log("============================================================================");
  console.log("  MEV DAO Arbitrage & Aave V3 Liquidation Searcher (https://mevdao.org)");
  console.log("============================================================================");

  const [blockNumber, uniReserves, sushiReserves] = await Promise.all([
    client.getBlockNumber(),
    client.readContract({
      address: WETH_USDC_UNIV2,
      abi: UNISWAP_V2_PAIR_ABI,
      functionName: "getReserves",
    }),
    client.readContract({
      address: WETH_USDC_SUSHIV2,
      abi: UNISWAP_V2_PAIR_ABI,
      functionName: "getReserves",
    }),
  ]);

  console.log(`Connected to Ethereum Mainnet at Block #${blockNumber}`);
  console.log(
    `Uniswap V2 WETH/USDC Reserves   : ${formatUnits(uniReserves[0], 6)} USDC / ${formatEther(uniReserves[1])} WETH`
  );
  console.log(
    `SushiSwap V2 WETH/USDC Reserves : ${formatUnits(sushiReserves[0], 6)} USDC / ${formatEther(sushiReserves[1])} WETH`
  );

  const testSize = parseEther("5");
  const arbAB = evaluateTwoPoolFlashArbitrage({
    flashLoanWethWei: testSize,
    poolA_ReserveUSDC: uniReserves[0],
    poolA_ReserveWETH: uniReserves[1],
    poolB_ReserveUSDC: sushiReserves[0],
    poolB_ReserveWETH: sushiReserves[1],
  });

  console.log(
    `\nSimulated 5 WETH Flash-Loan Arb (UniV2 -> SushiV2): Gross Return = ${formatEther(arbAB.grossWethReturned)} WETH | Net Delta = ${formatEther(arbAB.netProfitBeforeGasWei)} WETH`
  );
  console.log(
    "\nRead full guides at:\n - https://mevdao.org/blog/how-to-build-a-profitable-mev-arbitrage-bot\n - https://mevdao.org/blog/how-liquidation-bots-work-on-aave-and-why-they-re-hard-to-win"
  );
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch(console.error);
}
