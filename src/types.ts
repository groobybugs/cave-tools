export type TextContent = {
  type: "text";
  text: string;
};

export type ImageContent = {
  type: "image";
  data: string; // base64-encoded
  mimeType: string; // e.g. "image/png"
};

export type ResourceContent = {
  type: "resource";
  resource: {
    uri: string;
    mimeType: string; // e.g. "application/pdf"
    blob: string; // base64-encoded
  };
};

export interface ToolResult {
  content: Array<TextContent | ImageContent | ResourceContent>;
  isError?: boolean;
}
