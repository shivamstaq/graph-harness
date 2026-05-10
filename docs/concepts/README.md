# Concepts

The conceptual model behind Graph Harness: how the substrate is structured, how harnesses pin to regions of it, when their context attaches, and how every claim flows through one trust pipeline.

This section provides a high-level architectural overview for architects, DevOps teams, and enterprise evaluators.

## Core Concepts

| Concept | Description |
|---|---|
| [The layered graph](the-layered-graph.md) | How the substrate is structured. Covers the seven layers, the single global sequence, provenance folding, and replayability. |
| [Lifecycle attachment](lifecycle-attachment.md) | When and why a harness attaches its context to an operation. Covers the six phases and relation-aware blast radius traversal. |
| [Consistency across surfaces](consistency-across-surfaces.md) | How agents, humans, and CI read the same brief at a pinned sequence, and how they propose updates symmetrically. |
| [The trust pipeline](the-trust-pipeline.md) | The proposal lifecycle every claim travels through. Covers per-layer promotion modes, evidence requirements, and conflict resolution. |

## Reading Guide

For a complete understanding of the system's architecture, we recommend reading the pages in the order listed above. Each page is designed to be self-contained but builds conceptually on the previous ones.
