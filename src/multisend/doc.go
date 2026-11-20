// Package multisend is the SENDER side of croc's 1-to-N "multi-send" mode
// (owner: N, milestone M1c in docs/phan-cong.md).
//
// # The big picture
//
// Normal croc pairs exactly one sender with one receiver. Multi-send lets one
// sender push the same files to N receivers while uploading the bytes only
// ONCE. The trick is a "hub" that runs inside the relay (src/multihub, D's
// package): the sender uploads each encrypted chunk to the hub one time and the
// hub copies it to every receiver.
//
//	sender ──DATA──► hub ──DATA──► receiver 2
//	                     ├─DATA──► receiver 3
//	                     └─DATA──► receiver 4
//
// For that to work, every receiver must be able to decrypt the same
// ciphertext, so the sender picks one random 32-byte session key ("dataKey")
// and hands it to each receiver privately during the PAKE handshake
// (src/multisession, T's package). After that, data is encrypted exactly once.
//
// # What lives here
//
//   - client.go: Send, the full flow behind `croc send --receivers N`: dial the
//     relay, authenticate each receiver as it joins, wait for N "ready",
//     stream the files, then wait for every "done"/"fail".
//   - sender.go: StreamFiles, the loop that reads files, cuts them into 32 KB
//     chunks, hashes them, encrypts each record once, and hands the ciphertext
//     to a caller-supplied emit function (later: "send a DATA frame to the hub").
//   - types.go: SendOptions and PeerProgress.
//
// The record format inside each DATA frame (chunk / file-end / all-end) is in
// src/multiwire/record.go, shared with the receiver.
//
// # Layers, from outside in
//
// One chunk of a file on the wire looks like this:
//
//	comm frame:    "croc" | len u32 | ...                  (src/comm, existing)
//	 hub frame:    type u8 | flags u8 | from u16 | to u16 | ...   (src/multiwire)
//	  DATA payload: nonce(12) | AES-GCM ciphertext | tag(16) (src/crypt)
//	   record:      kind u8 | fileIdx u16 | offset u64 | data   (src/multiwire)
//
// The hub only ever looks at the hub-frame header; everything from the nonce
// inward is opaque to it.
package multisend
