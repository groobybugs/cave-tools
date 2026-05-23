export interface ToolResult {
  content: Array<{
    type: "text";
    text: string;
  }>;
  isError?: boolean;
}
