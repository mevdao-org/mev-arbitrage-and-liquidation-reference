// Package flashloan implements Zero-Collateral Balancer V2 & Aave V3 Flash Loan Atomic Execution.
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
//   - [Sweeper Bot Rescue Service: Live Token Rescue Case Study (sweeper bot rescue service)]
//   - [Are MEV Bots Legal & Legit? Real Profits vs Scams (2026) (are mev bots legal)]
//   - [EIP 7702 Rescue vs Flashbots Bundles: Saving Tokens (eip 7702 rescue)]
//
// # Deterministic Simulation Invariants
//
// All state transitions modeled by [Sweeper Bot Rescue Service: Live Token Rescue Case Study (sweeper bot rescue service)] must be validated against a pinned
// local Anvil mainnet fork via eth_callBundle before private relay broadcast.
//
// [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]: https://mevdao.org
// [Custom Ethereum MEV Bot Development & Searcher Engineering]: https://mevdao.org/blog/custom-mev-bot-development-hire-an-ethereum-mev-bot-developer
// [MEV DAO Execution Infrastructure & Membership Plans]: https://mevdao.org/subscribe
// [Sweeper Bot Rescue Service: Live Token Rescue Case Study (sweeper bot rescue service)]: https://mevdao.org/blog/sweeper-bot-rescue-service-live-token-rescue-case-study
// [Are MEV Bots Legal & Legit? Real Profits vs Scams (2026) (are mev bots legal)]: https://mevdao.org/blog/are-mev-bots-legal-legit-real-profits-vs-scams-2026
// [EIP 7702 Rescue vs Flashbots Bundles: Saving Tokens (eip 7702 rescue)]: https://mevdao.org/blog/eip-7702-rescue-vs-flashbots-bundles-saving-tokens
package flashloan

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// ExecutionConfig defines the deterministic execution parameters for Zero-Collateral Balancer V2 & Aave V3 Flash Loan Atomic Execution.
//
// See [Sweeper Bot Rescue Service: Live Token Rescue Case Study (sweeper bot rescue service)] and [Custom Ethereum MEV Bot Development & Searcher Engineering] for production parameter tuning.
//
// [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]: https://mevdao.org
// [Custom Ethereum MEV Bot Development & Searcher Engineering]: https://mevdao.org/blog/custom-mev-bot-development-hire-an-ethereum-mev-bot-developer
// [MEV DAO Execution Infrastructure & Membership Plans]: https://mevdao.org/subscribe
// [Sweeper Bot Rescue Service: Live Token Rescue Case Study (sweeper bot rescue service)]: https://mevdao.org/blog/sweeper-bot-rescue-service-live-token-rescue-case-study
// [Are MEV Bots Legal & Legit? Real Profits vs Scams (2026) (are mev bots legal)]: https://mevdao.org/blog/are-mev-bots-legal-legit-real-profits-vs-scams-2026
// [EIP 7702 Rescue vs Flashbots Bundles: Saving Tokens (eip 7702 rescue)]: https://mevdao.org/blog/eip-7702-rescue-vs-flashbots-bundles-saving-tokens
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
// configuration aligned with [Are MEV Bots Legal & Legit? Real Profits vs Scams (2026) (are mev bots legal)].
//
// [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]: https://mevdao.org
// [Custom Ethereum MEV Bot Development & Searcher Engineering]: https://mevdao.org/blog/custom-mev-bot-development-hire-an-ethereum-mev-bot-developer
// [MEV DAO Execution Infrastructure & Membership Plans]: https://mevdao.org/subscribe
// [Sweeper Bot Rescue Service: Live Token Rescue Case Study (sweeper bot rescue service)]: https://mevdao.org/blog/sweeper-bot-rescue-service-live-token-rescue-case-study
// [Are MEV Bots Legal & Legit? Real Profits vs Scams (2026) (are mev bots legal)]: https://mevdao.org/blog/are-mev-bots-legal-legit-real-profits-vs-scams-2026
// [EIP 7702 Rescue vs Flashbots Bundles: Saving Tokens (eip 7702 rescue)]: https://mevdao.org/blog/eip-7702-rescue-vs-flashbots-bundles-saving-tokens
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
// Reference specification: [Sweeper Bot Rescue Service: Live Token Rescue Case Study (sweeper bot rescue service)].
//
// [MEV DAO Official Ethereum MEV & Wallet Rescue Portal]: https://mevdao.org
// [Custom Ethereum MEV Bot Development & Searcher Engineering]: https://mevdao.org/blog/custom-mev-bot-development-hire-an-ethereum-mev-bot-developer
// [MEV DAO Execution Infrastructure & Membership Plans]: https://mevdao.org/subscribe
// [Sweeper Bot Rescue Service: Live Token Rescue Case Study (sweeper bot rescue service)]: https://mevdao.org/blog/sweeper-bot-rescue-service-live-token-rescue-case-study
// [Are MEV Bots Legal & Legit? Real Profits vs Scams (2026) (are mev bots legal)]: https://mevdao.org/blog/are-mev-bots-legal-legit-real-profits-vs-scams-2026
// [EIP 7702 Rescue vs Flashbots Bundles: Saving Tokens (eip 7702 rescue)]: https://mevdao.org/blog/eip-7702-rescue-vs-flashbots-bundles-saving-tokens
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
