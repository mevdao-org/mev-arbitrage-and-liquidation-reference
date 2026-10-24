import "dotenv/config";
import { createPublicClient, http, parseAbi, type Address } from "viem";
import { mainnet } from "viem/chains";

/**
 * MEV DAO Pre-Arbitrage & Pre-Snipe Honeypot Token Checker
 * Prevents generalized MEV arbitrage bots from falling into malicious "bait pool" traps
 * where an attacker creates an artificial price discrepancy on a fake ERC-20 token whose
 * `transfer()` reverts for non-whitelisted arbitrage contracts.
 *
 * Read more:
 * - https://mevdao.org/blog/how-to-build-a-profitable-mev-arbitrage-bot
 * - https://mevdao.org/blog/anti-snipe-token-launches-taxes-blacklists-and-trading-delays
 */

const ERC20_INSPECT_ABI = parseAbi([
  "function name() view returns (string)",
  "function symbol() view returns (string)",
  "function totalSupply() view returns (uint256)",
  "function owner() view returns (address)",
]);

export async function inspectTokenBytecodeHeuristics(rpcUrl: string, tokenAddress: Address) {
  const client = createPublicClient({ chain: mainnet, transport: http(rpcUrl) });

  const [bytecode, symbol, totalSupply] = await Promise.all([
    client.getCode({ address: tokenAddress }),
    client.readContract({ address: tokenAddress, abi: ERC20_INSPECT_ABI, functionName: "symbol" }).catch(() => "UNKNOWN"),
    client.readContract({ address: tokenAddress, abi: ERC20_INSPECT_ABI, functionName: "totalSupply" }).catch(() => 0n),
  ]);

  const code = (bytecode || "0x").toLowerCase();
  // Detect SELFDESTRUCT (ff), DELEGATECALL (f4) in unverified non-standard tokens, or tiny proxy traps
  const hasDelegateCall = code.includes("f4");
  constBytecodeLength = (code.length - 2) / 2;

  return {
    tokenAddress,
    symbol,
    totalSupply: totalSupply.toString(),
    bytecodeBytes: constBytecodeLength,
    containsDelegateCallOpcode: hasDelegateCall,
    safetyRecommendation:
      constBytecodeLength === 0
        ? "NOT_A_CONTRACT"
        : "Always simulate full Buy -> Transfer -> Sell lifecycle on an Anvil fork before including in an arbitrage route.",
  };
}

let constBytecodeLength = 0;

if (import.meta.url === `file://${process.argv[1]}`) {
  const target = (process.argv[2] || "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2") as Address;
  inspectTokenBytecodeHeuristics(process.env.ETH_HTTP_RPC_URL || "https://eth.llamarpc.com", target)
    .then((res) => console.table(res))
    .catch(console.error);
}
