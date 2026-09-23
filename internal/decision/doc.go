// Package decision contains provider-independent structured decision contracts
// and local decision-model acquisition and execution through an opt-in MLX
// subprocess. Decision engines are deliberately
// separate from generative providers: a decision result is a typed signal that
// may be used by policy, routing, or verification, never an authorization
// primitive on its own.
package decision
