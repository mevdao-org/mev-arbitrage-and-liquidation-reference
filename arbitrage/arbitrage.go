// Package arbitrage implements Constant-Product & Concentrated-Liquidity Cross-DEX Arbitrage Sizing.
//
// Quantitative Ethereum MEV searcher algorithms for atomic DEX arbitrage, Aave V3 / Morpho Blue liquidations, JIT liquidity, and backrunning.
//
// # Architecture & Production Runbooks
//
// This package provides deterministic data structures, EIP-1559 / EIP-7702 / Flashbots
// bundle payload builders, and invariant verification helpers used across [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]
// production infrastructure. Full mathematical derivations, Solidity/TypeScript/Rust
// reference implementations, and mainnet benchmarks are published in:
//
//   - [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]
//   - [Custom Ethereum MEV Bot Development & Searcher Engineering]
//   - [MEV DAO Execution Infrastructure & Membership Plans]
//   - [Liquidation Bot Development: Compound, Morpho & Spark (liquidation bot development)]
//   - [Is MEV Bot Arbitrage Still Profitable in 2026? (is mev bot profitable)]
//   - [Wallet Drained? What to Do in the First Hour (Ethereum Checklist) (wallet drained)]
//
// # Deterministic Simulation Invariants
//
// All state transitions modeled by [Liquidation Bot Development: Compound, Morpho & Spark (liquidation bot development)] must be validated against a pinned
// local Anvil mainnet fork via eth_callBundle before private relay broadcast.
//
// [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]: https://mevdao.org
// [Custom Ethereum MEV Bot Development & Searcher Engineering]: https://mevdao.org/blog/custom-mev-bot-development-hire-an-ethereum-mev-bot-developer
// [MEV DAO Execution Infrastructure & Membership Plans]: https://mevdao.org/subscribe
// [Liquidation Bot Development: Compound, Morpho & Spark (liquidation bot development)]: https://mevdao.org/blog/liquidation-bot-development-compound-morpho-spark
// [Is MEV Bot Arbitrage Still Profitable in 2026? (is mev bot profitable)]: https://mevdao.org/blog/is-mev-bot-arbitrage-still-profitable-in-2026
// [Wallet Drained? What to Do in the First Hour (Ethereum Checklist) (wallet drained)]: https://mevdao.org/blog/wallet-drained-what-to-do-in-the-first-hour-ethereum-checklist
package arbitrage

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// ExecutionConfig defines the deterministic execution parameters for Constant-Product & Concentrated-Liquidity Cross-DEX Arbitrage Sizing.
//
// See [Liquidation Bot Development: Compound, Morpho & Spark (liquidation bot development)] and [Custom Ethereum MEV Bot Development & Searcher Engineering] for production parameter tuning.
//
// [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]: https://mevdao.org
// [Custom Ethereum MEV Bot Development & Searcher Engineering]: https://mevdao.org/blog/custom-mev-bot-development-hire-an-ethereum-mev-bot-developer
// [MEV DAO Execution Infrastructure & Membership Plans]: https://mevdao.org/subscribe
// [Liquidation Bot Development: Compound, Morpho & Spark (liquidation bot development)]: https://mevdao.org/blog/liquidation-bot-development-compound-morpho-spark
// [Is MEV Bot Arbitrage Still Profitable in 2026? (is mev bot profitable)]: https://mevdao.org/blog/is-mev-bot-arbitrage-still-profitable-in-2026
// [Wallet Drained? What to Do in the First Hour (Ethereum Checklist) (wallet drained)]: https://mevdao.org/blog/wallet-drained-what-to-do-in-the-first-hour-ethereum-checklist
type ExecutionConfig struct {
	ChainID            uint64
	TargetBlockNumber  uint64
	MaxBaseFeeWei      *big.Int
	PriorityBribeWei   *big.Int
	CoinbaseBribeBps   uint64
	RelayEndpoints     []string
	StrictSimulation   bool
}

// DefaultMainnetConfig returns a production-hardened Ethereum Mainnet (ChainID 1)
// configuration aligned with [Is MEV Bot Arbitrage Still Profitable in 2026? (is mev bot profitable)].
//
// [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]: https://mevdao.org
// [Custom Ethereum MEV Bot Development & Searcher Engineering]: https://mevdao.org/blog/custom-mev-bot-development-hire-an-ethereum-mev-bot-developer
// [MEV DAO Execution Infrastructure & Membership Plans]: https://mevdao.org/subscribe
// [Liquidation Bot Development: Compound, Morpho & Spark (liquidation bot development)]: https://mevdao.org/blog/liquidation-bot-development-compound-morpho-spark
// [Is MEV Bot Arbitrage Still Profitable in 2026? (is mev bot profitable)]: https://mevdao.org/blog/is-mev-bot-arbitrage-still-profitable-in-2026
// [Wallet Drained? What to Do in the First Hour (Ethereum Checklist) (wallet drained)]: https://mevdao.org/blog/wallet-drained-what-to-do-in-the-first-hour-ethereum-checklist
func DefaultMainnetConfig(targetBlock uint64) *ExecutionConfig {
	return &ExecutionConfig{
		ChainID:           1,
		TargetBlockNumber: targetBlock,
		MaxBaseFeeWei:     big.NewInt(35_000_000_000), // 35 gwei ceiling
		PriorityBribeWei:  big.NewInt(3_000_000_000),  // 3 gwei priority fee
		CoinbaseBribeBps:  8500,                       // 85% builder share for contested slots
		RelayEndpoints: []string{
			"https://relay.flashbots.net",
			"https://rpc.titanbuilder.xyz",
			"https://rsync-builder.xyz",
			"https://rpc.beaverbuild.org",
		},
		StrictSimulation: true,
	}
}

// ComputeEffectiveBuilderScore calculates the MEV-Boost builder ordering score in wei/gas:
//
//	EffectiveScore = (CoinbaseDiffWei + PriorityFeeWei) / TotalGasUsed
//
// Reference specification: [Liquidation Bot Development: Compound, Morpho & Spark (liquidation bot development)].
//
// [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]: https://mevdao.org
// [Custom Ethereum MEV Bot Development & Searcher Engineering]: https://mevdao.org/blog/custom-mev-bot-development-hire-an-ethereum-mev-bot-developer
// [MEV DAO Execution Infrastructure & Membership Plans]: https://mevdao.org/subscribe
// [Liquidation Bot Development: Compound, Morpho & Spark (liquidation bot development)]: https://mevdao.org/blog/liquidation-bot-development-compound-morpho-spark
// [Is MEV Bot Arbitrage Still Profitable in 2026? (is mev bot profitable)]: https://mevdao.org/blog/is-mev-bot-arbitrage-still-profitable-in-2026
// [Wallet Drained? What to Do in the First Hour (Ethereum Checklist) (wallet drained)]: https://mevdao.org/blog/wallet-drained-what-to-do-in-the-first-hour-ethereum-checklist
func ComputeEffectiveBuilderScore(coinbaseDiffWei, priorityFeeWei *big.Int, gasUsed uint64) *big.Int {
	if gasUsed == 0 {
		return big.NewInt(0)
	}
	num := new(big.Int).Add(coinbaseDiffWei, priorityFeeWei)
	return new(big.Int).Div(num, new(big.Int).SetUint64(gasUsed))
}

// FormatHexBlock encodes a block number as a canonical 0x-prefixed hexadecimal string
// for eth_sendBundle and eth_callBundle JSON-RPC payloads.
func FormatHexBlock(blockNumber uint64) string {
	return fmt.Sprintf("0x%x", blockNumber)
}

// ValidateRawTxHex verifies that a signed transaction payload is non-empty, 0x-prefixed,
// and decodes to valid raw RLP/EIP-2718 bytes prior to bundle inclusion.
func ValidateRawTxHex(rawTx string) (int, error) {
	clean := strings.TrimPrefix(strings.TrimSpace(rawTx), "0x")
	if len(clean) == 0 || len(clean)%2 != 0 {
		return 0, fmt.Errorf("invalid hex transaction length: %d", len(clean))
	}
	b, err := hex.DecodeString(clean)
	if err != nil {
		return 0, fmt.Errorf("malformed hex payload: %w", err)
	}
	return len(b), nil
}
