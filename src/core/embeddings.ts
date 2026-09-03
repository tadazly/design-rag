import type { EmbeddingConfig } from "../shared/contracts.js";
import type { EmbeddingProvider } from "./types.js";

interface OllamaEmbedResponse {
  embeddings?: number[][];
}

export class OllamaEmbeddingProvider implements EmbeddingProvider {
  readonly id: string;

  constructor(private readonly config: EmbeddingConfig) {
    this.id = `ollama:${config.model}`;
  }

  async isAvailable(signal?: AbortSignal): Promise<boolean> {
    try {
      const result = await this.embed(["drag 游戏策划语义检索探测"], signal);
      return result.length === 1 && (result[0]?.length ?? 0) > 0;
    } catch {
      return false;
    }
  }

  async embed(input: string[], signal?: AbortSignal): Promise<number[][]> {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(new Error("Ollama embedding 请求超时")), this.config.timeoutMs);
    const abort = () => controller.abort(signal?.reason);
    signal?.addEventListener("abort", abort, { once: true });
    try {
      const response = await fetch(this.config.endpoint, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ model: this.config.model, input, truncate: true }),
        signal: controller.signal,
      });
      if (!response.ok) throw new Error(`Ollama embedding 失败：HTTP ${response.status}`);
      const payload = (await response.json()) as OllamaEmbedResponse;
      if (!payload.embeddings || payload.embeddings.length !== input.length) {
        throw new Error("Ollama embedding 返回数量不匹配");
      }
      return payload.embeddings;
    } finally {
      clearTimeout(timeout);
      signal?.removeEventListener("abort", abort);
    }
  }
}

export function cosineSimilarity(left: number[], right: number[]): number {
  if (left.length === 0 || left.length !== right.length) return 0;
  let dot = 0;
  let leftNorm = 0;
  let rightNorm = 0;
  for (let index = 0; index < left.length; index += 1) {
    const a = left[index] ?? 0;
    const b = right[index] ?? 0;
    dot += a * b;
    leftNorm += a * a;
    rightNorm += b * b;
  }
  if (leftNorm === 0 || rightNorm === 0) return 0;
  return dot / Math.sqrt(leftNorm * rightNorm);
}
