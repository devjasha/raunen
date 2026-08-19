package mcp

// ServerCapabilities is the subset of the server's advertised capabilities that
// raunen cares about. It is decoded from the initialize result; the rest of the
// capability object (prompts, resources, logging, ...) is ignored because raunen
// only drives tools.
type ServerCapabilities struct {
	// Tools reports whether the server offers tools. ListChanged, when set,
	// means the server will send tools/list_changed notifications so raunen can
	// refresh its toolset without polling. The reconnection/SSE work picks this
	// up to know whether a live refresh is even possible.
	Tools struct {
		ListChanged bool `json:"listChanged"`
	} `json:"tools"`
}

// InitializeResult is the body of the initialize response. raunen records the
// negotiated protocol version and the server's capabilities so later code can
// adapt to what the server actually supports rather than what we asked for.
type InitializeResult struct {
	// ProtocolVersion is the version the server will speak — not necessarily the
	// one we offered. A server may answer with an older version it supports.
	ProtocolVersion string `json:"protocolVersion"`
	// Capabilities is what the server claims to support.
	Capabilities ServerCapabilities `json:"capabilities"`
	// ServerInfo identifies the server for logging; we do not act on it.
	ServerInfo struct {
		Name string `json:"name"`
	} `json:"serverInfo"`
}

// ToolAnnotations are the optional hints a server attaches to a tool in
// tools/list. They are advisory metadata. raunen lets only readOnlyHint drive
// the Mutates gate; the others are decoded and kept so follow-up work can
// surface them, but none of them force Mutates to true.
type ToolAnnotations struct {
	// ReadOnlyHint, when true, declares the tool leaves its environment
	// untouched. When false or absent raunen treats the tool as mutating — the
	// safe default for an untrusted peer. This is the one hint that gates
	// behaviour.
	ReadOnlyHint *bool `json:"readOnlyHint,omitempty"`
	// DestructiveHint, when true, warns the tool may irreversibly alter state.
	// Advisory only; it never relaxes the mutating default.
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	// OpenWorldHint, when true, warns the tool interacts with an "open world" of
	// external effects. Advisory only.
	OpenWorldHint *bool `json:"openWorldHint,omitempty"`
}
