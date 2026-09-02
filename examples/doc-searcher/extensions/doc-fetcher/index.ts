import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import https from "node:https";

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: "fetch_docs",
    label: "fetch_docs",
    description: "Fetch documentation pages from allowed documentation endpoints",
    parameters: {
      type: "object",
      properties: {
        path: {
          type: "string",
          description: "Documentation path to retrieve from docs.github.com",
        },
      },
      required: ["path"],
    },
    execute: async (_toolCallId, params) => {
      const docPath = (params as { path: string }).path;
      return new Promise((resolve) => {
        const options = {
          hostname: "docs.github.com",
          port: 443,
          path: docPath.startsWith("/") ? docPath : `/${docPath}`,
          method: "GET",
          headers: {
            "User-Agent": "AgentRun-DocSearcher/1.0",
          },
        };

        const req = https.request(options, (res) => {
          let data = "";
          res.on("data", (chunk) => {
            data += chunk;
          });
          res.on("end", () => {
            resolve({
              content: [
                {
                  type: "text",
                  text: data.slice(0, 8000),
                },
              ],
            });
          });
        });

        req.on("error", (err) => {
          resolve({
            isError: true,
            content: [
              {
                type: "text",
                text: `Documentation fetch failed: ${err.message}`,
              },
            ],
          });
        });

        req.end();
      });
    },
  });
}
