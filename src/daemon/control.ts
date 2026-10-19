import net from "net";
import { buildId, socketPath } from "./paths.js";

// `cave-tools daemon status|stop`: talk to a running daemon without spawning one.

function rpc(method: string): Promise<{ result?: { pid?: number; buildId?: string }; error?: string } | null> {
  return new Promise((resolve) => {
    const socket = net.createConnection(socketPath());
    let buffer = "";
    const timer = setTimeout(() => {
      socket.destroy();
      resolve(null);
    }, 2000);
    socket.setEncoding("utf8");
    socket.once("connect", () => {
      socket.write(`${JSON.stringify({ id: 1, method: "hello", clientPid: process.pid })}\n`);
      if (method !== "hello") socket.write(`${JSON.stringify({ id: 2, method })}\n`);
    });
    socket.on("data", (chunk: string) => {
      buffer += chunk;
      for (const line of buffer.split("\n").slice(0, -1)) {
        const msg = JSON.parse(line);
        if (msg.id === (method === "hello" ? 1 : 2)) {
          clearTimeout(timer);
          socket.destroy();
          resolve(msg);
        }
      }
      buffer = buffer.slice(buffer.lastIndexOf("\n") + 1);
    });
    socket.once("error", () => {
      clearTimeout(timer);
      resolve(null);
    });
  });
}

export async function daemonStatus(): Promise<number> {
  const reply = await rpc("hello");
  if (!reply?.result) {
    console.log(`not running (socket ${socketPath()}, build ${buildId()})`);
    return 1;
  }
  console.log(`running pid ${reply.result.pid} build ${reply.result.buildId} socket ${socketPath()}`);
  return 0;
}

export async function daemonStop(): Promise<number> {
  const reply = await rpc("shutdown");
  if (!reply?.result) {
    console.log("not running");
    return 1;
  }
  console.log("stopped");
  return 0;
}
