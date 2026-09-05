---
kind: work-log
title: A node nobody polls is indistinguishable from a node that is idle
date: 2026-09-05
subject: Bostonvex/machinist#76
---

## What happened

This machine reaches two GB10 nodes and the collector polled one of them.
`[collector.nvidia_remote]` was a single table held as a single pointer, so
`spark-27c2` was invisible — which is the one state indistinguishable from
idle, and precisely the failure that splitting `[collector.nvidia]` from
`[collector.nvidia_remote]` was meant to prevent.

`[[collector.nvidia_remote]]` now repeats, once per node. The single-table form
still loads unchanged, so a deployment with one remote node does not have to be
rewritten to stay where it is.

## The rule that gave, and the one that did not

The documented rule was "each provider table may appear at most once, because
two providers under one name would share a status row and an operator reading it
could not tell which of them was failing". That reasoning is right and it
survives. It was standing in for something narrower than it said: what has to
hold is one status row per *named thing*.

`node_id` is that name. Two entries may not share one, and the check spans the
local and remote tables together — a sample carries a node_id and nothing that
says whether it was read here or over SSH, so `[collector.nvidia]` and a remote
node are in the same namespace. Names that differ only in padding are one name
to whoever reads the board, so they collide too. With the name secured, the
count of named things is free to be the count that exists.

A second remote node has to be named explicitly. `remote-nvidia` is a name for
*the* remote node and stops being one the moment there are two, and defaulting
past that would hand two machines one identity on the operator's behalf and then
report a collision between two lines that mention no node_id at all.

## Where the invariant actually lives

`provider.NewSupervisor` already refuses two providers under one `Name()`, and
`Nvidia.Name()` returned `"nvidia-smi-remote"` for every remote node. A config
that permitted two nodes without changing that would have loaded and then
refused to start. The name is now `nvidia-smi-remote:<node_id>`, which is a
visible change to `/healthz` for single-node deployments and the right one: an
operator reading a failing poller has to be able to tell which machine stopped
answering.

## The decoder

`CollectorNvidiaNodes` implements `UnmarshalTOML`, which go-toml calls once for
a table and once per entry for a table array — so appending handles both forms
without either knowing about the other. Inline tables and inline arrays are
given a key to belong to before parsing, since they are values rather than
documents.

The fragment is decoded strictly. The outer decoder's `DisallowUnknownFields`
does not reach inside a type that decodes itself, and bite-checking proved it:
with the inner strictness removed, `ssh_hostname = "spark.local"` was refused
only because `ssh_host` was then missing — a different check, and one that a
node with a valid host and a spurious `ssh_port` would sail straight past. The
test was rewritten to name the field it is refusing so the two cannot be
confused again.

## What is left

Nothing for this. The live config now names both nodes.
