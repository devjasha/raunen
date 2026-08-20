package acp

import "encoding/json"

// The wire types, named and tagged to match the ACP schema exactly.
//
// Field names are camelCase and the tagged unions carry a discriminator:
// content blocks and tool-call content use "type", session updates use
// "sessionUpdate". Getting these wrong produces a connection that looks fine
// and silently does nothing, so they are written out rather than derived.

// Version is the protocol level this implements. ACP negotiates an integer;
// the client sends the newest it knows and the agent answers with what it will
// actually speak.
const Version = 1

// Implementation names a side of the connection, for logs and about-boxes.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// InitializeRequest opens the connection.
type InitializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
}

// ClientCapabilities is what the editor can do for us. We record it but lean on
// it lightly: raunen's tools reach the filesystem directly, since it runs on
// the same machine as the editor and the working directory is its own.
type ClientCapabilities struct {
	FS       FSCapabilities `json:"fs"`
	Terminal bool           `json:"terminal,omitempty"`
}

// FSCapabilities reports whether the editor will read and write files for us.
type FSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// InitializeResponse answers with the version and what this agent supports.
type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []AuthMethod      `json:"authMethods"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
}

// AgentCapabilities declares what the editor may ask for.
type AgentCapabilities struct {
	// LoadSession says session/load works, which it does: raunen has saved
	// sessions on disk already and resuming one is what --continue does.
	LoadSession        bool               `json:"loadSession"`
	PromptCapabilities PromptCapabilities `json:"promptCapabilities"`
	MCPCapabilities    MCPCapabilities    `json:"mcpCapabilities"`
}

// PromptCapabilities says what a prompt may contain.
//
// Image and audio are false: raunen's provider client sends message content as
// a plain string, and models are filtered to those that can answer a chat turn.
// Claiming otherwise would have an editor send an attachment that silently
// vanished, which is worse than the editor greying the button out.
type PromptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

// MCPCapabilities says which MCP transports a client may hand over with a new
// session. raunen speaks all three, but see newSession for why they are not
// accepted from the client.
type MCPCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

// AuthMethod is a way to log in. raunen has none: it talks to whatever endpoint
// the config names, and a key belongs in the config or the environment rather
// than in a protocol handshake.
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// NewSessionRequest asks for a session rooted at a directory.
type NewSessionRequest struct {
	Cwd                   string      `json:"cwd"`
	AdditionalDirectories []string    `json:"additionalDirectories,omitempty"`
	MCPServers            []MCPServer `json:"mcpServers,omitempty"`
}

// MCPServer is a server the client offers. Accepted and ignored; see
// newSession.
type MCPServer struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	URL  string `json:"url,omitempty"`
}

// NewSessionResponse hands back the session's id and the modes it offers.
type NewSessionResponse struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"`
}

// LoadSessionRequest reopens a saved session.
type LoadSessionRequest struct {
	SessionID  string      `json:"sessionId"`
	Cwd        string      `json:"cwd"`
	MCPServers []MCPServer `json:"mcpServers,omitempty"`
}

// LoadSessionResponse mirrors NewSessionResponse.
type LoadSessionResponse struct {
	Modes *SessionModeState `json:"modes,omitempty"`
}

// SessionModeState is the mode in force and what else can be chosen.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SessionMode is one selectable mode.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SetModeRequest switches mode mid-session.
type SetModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// PromptRequest is a turn.
type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// PromptResponse ends a turn and says why it ended.
type PromptResponse struct {
	StopReason string `json:"stopReason"`
	Usage      *Usage `json:"usage,omitempty"`
}

// Stop reasons, as ACP spells them.
const (
	StopEndTurn         = "end_turn"
	StopMaxTokens       = "max_tokens"
	StopMaxTurnRequests = "max_turn_requests"
	StopRefusal         = "refusal"
	StopCancelled       = "cancelled"
)

// Usage reports what a turn cost.
type Usage struct {
	TotalTokens  int `json:"totalTokens"`
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// CancelNotification asks for the running turn to stop. It is a notification,
// not a call: the turn ends by returning its own response with a cancelled stop
// reason, which is how ACP models it.
type CancelNotification struct {
	SessionID string `json:"sessionId"`
}

// ContentBlock is a piece of a prompt or of a tool result. The discriminator is
// "type"; only the variants raunen can act on are modelled.
type ContentBlock struct {
	Type string `json:"type"`
	// Text carries the "text" variant.
	Text string `json:"text,omitempty"`
	// URI and Name describe a "resource_link".
	URI  string `json:"uri,omitempty"`
	Name string `json:"name,omitempty"`
	// Resource carries an embedded "resource", whose text is inlined by the
	// editor. Kept raw so an unfamiliar shape is passed through rather than
	// dropped on the floor.
	Resource json.RawMessage `json:"resource,omitempty"`
}

// TextBlock builds the content block for a piece of prose.
func TextBlock(s string) ContentBlock { return ContentBlock{Type: "text", Text: s} }

// SessionNotification wraps every streamed update. The session id is repeated
// on each one because a client may hold several sessions on one connection.
type SessionNotification struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

// ContentChunk is a fragment of a message being streamed.
type ContentChunk struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       ContentBlock `json:"content"`
}

// The sessionUpdate discriminators.
const (
	updateAgentMessage = "agent_message_chunk"
	updateAgentThought = "agent_thought_chunk"
	updateToolCall     = "tool_call"
	updateToolCallEnd  = "tool_call_update"
	updateCurrentMode  = "current_mode_update"
)

// ToolCallNotice announces a tool call.
type ToolCallNotice struct {
	SessionUpdate string             `json:"sessionUpdate"`
	ToolCallID    string             `json:"toolCallId"`
	Title         string             `json:"title"`
	Name          string             `json:"name,omitempty"`
	Kind          string             `json:"kind,omitempty"`
	Status        string             `json:"status,omitempty"`
	RawInput      json.RawMessage    `json:"rawInput,omitempty"`
	Locations     []ToolCallLocation `json:"locations,omitempty"`
}

// ToolCallLocation points the editor at the file a call is about, which is what
// lets it follow along in the buffer.
type ToolCallLocation struct {
	Path string `json:"path"`
}

// ToolCallUpdate revises a call already announced — normally to report that it
// finished and what it produced.
type ToolCallUpdate struct {
	SessionUpdate string            `json:"sessionUpdate"`
	ToolCallID    string            `json:"toolCallId"`
	Status        string            `json:"status,omitempty"`
	Content       []ToolCallContent `json:"content,omitempty"`
}

// ToolCallContent is what a call produced.
type ToolCallContent struct {
	Type    string       `json:"type"`
	Content ContentBlock `json:"content,omitempty"`
}

// Tool call statuses.
const (
	statusPending    = "pending"
	statusInProgress = "in_progress"
	statusCompleted  = "completed"
	statusFailed     = "failed"
)

// CurrentModeUpdate tells the client the mode changed underneath it.
type CurrentModeUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	CurrentModeID string `json:"currentModeId"`
}

// RequestPermissionRequest asks the editor whether a tool may run. This is the
// one call that travels agent-to-client during a turn.
type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOption is one answer the editor may offer.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// Permission option kinds, as ACP spells them.
const (
	kindAllowOnce   = "allow_once"
	kindAllowAlways = "allow_always"
	kindRejectOnce  = "reject_once"
)

// RequestPermissionResponse carries what the user chose. The outcome is nested
// because the result of a JSON-RPC call has to be an object.
type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome is either "cancelled" or "selected" with an option id.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}
