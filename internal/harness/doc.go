// Package harness implements opt-in, single-step harness experiments.
//
// It owns declarative snapshots, fixture comparisons and a private experience
// library. It does not own agent execution, permissions, tool admission, user
// memory or resumable agent runs. Only a closed catalog of reviewed guidance
// can reach prompts; proposer explanations are transient untrusted data.
// Probes execute trusted fixture scripts only, never arbitrary model actions.
// This is capability restriction, not process isolation or live-model evidence.
// Nothing imports tui or loads state during ordinary chat startup.
package harness
