import { parentPort } from "node:worker_threads";
import {
  indexTaskErrorCode,
  processIndexTask,
  type IndexWorkerRequest,
  type IndexWorkerResponse,
} from "./index-task.js";

const port = parentPort;
if (!port) throw new Error("索引 worker 必须由 worker_threads 启动");

port.on("message", async (request: IndexWorkerRequest) => {
  let response: IndexWorkerResponse;
  try {
    response = { id: request.id, ok: true, result: await processIndexTask(request.input) };
  } catch (error) {
    response = {
      id: request.id,
      ok: false,
      code: indexTaskErrorCode(error),
      message: error instanceof Error ? error.message : String(error),
      ...(error instanceof Error && error.stack ? { stack: error.stack } : {}),
    };
  }
  port.postMessage(response);
});
