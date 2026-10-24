// Package types holds the MCP result shapes shared by every cave-tools tool.
//
// Port of src/types.ts. The JSON produced here must stay byte-compatible with
// the TypeScript implementation so a client cannot tell which one answered.
package types

// Content is one MCP content block: text, image or resource.
// TS: `TextContent | ImageContent | ResourceContent`.
type Content interface {
	// contentType returns the TS union member's "type" discriminator.
	contentType() string
}

// TextContent is a plain text block.
// port of src/types.ts:TextContent
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (TextContent) contentType() string { return "text" }

// ImageContent is a base64-encoded image block.
// port of src/types.ts:ImageContent
type ImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`     // base64-encoded
	MimeType string `json:"mimeType"` // e.g. "image/png"
}

func (ImageContent) contentType() string { return "image" }

// Resource is the inline resource payload of a ResourceContent block.
// port of src/types.ts:ResourceContent["resource"]
type Resource struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType"` // e.g. "application/pdf"
	Blob     string `json:"blob"`     // base64-encoded
}

// ResourceContent is an embedded binary resource block.
// port of src/types.ts:ResourceContent
type ResourceContent struct {
	Type     string   `json:"type"`
	Resource Resource `json:"resource"`
}

func (ResourceContent) contentType() string { return "resource" }

// ToolResult is what every tool returns to the MCP layer.
//
// TS declares isError and _meta as optional. Go encodes an unset isError as
// absent (omitempty) which matches every TS call site: the TS code only ever
// sets isError to true, never to false.
// port of src/types.ts:ToolResult
type ToolResult struct {
	Content []Content      `json:"content"`
	IsError bool           `json:"isError,omitempty"`
	Meta    map[string]any `json:"_meta,omitempty"`
}

// Text builds a text content block. TS call sites write
// `{ type: "text", text }` inline; the constructors below keep the
// discriminator correct for every Go caller.
func Text(text string) TextContent { return TextContent{Type: "text", Text: text} }

// Image builds an image content block.
func Image(data, mimeType string) ImageContent {
	return ImageContent{Type: "image", Data: data, MimeType: mimeType}
}

// ResourceBlock builds a resource content block.
func ResourceBlock(uri, mimeType, blob string) ResourceContent {
	return ResourceContent{
		Type:     "resource",
		Resource: Resource{URI: uri, MimeType: mimeType, Blob: blob},
	}
}
