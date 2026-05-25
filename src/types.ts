export type TextContent = {
  type: "text";
  text: string;
};

export type ImageContent = {
  type: "image";
  data: string; // base64-encoded
  mimeType: string; // e.g. "image/png"
};

export interface ToolResult {
  content: Array<TextContent | ImageContent>;
  isError?: boolean;
}
