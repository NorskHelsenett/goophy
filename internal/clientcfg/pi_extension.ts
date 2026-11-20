/**
 * Goophy provider for pi. Written by `goophy configure pi`; rerun it to update.
 *
 * Lists the chat models the local goophy serves (it forwards to the NHN LLM
 * gateway), skipping embedding and reranker models. When goophy is not
 * running at startup, a short static list is used so pi still starts.
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const BASE_URL = "{{BASE_URL}}";
const MODELS_URL = `${BASE_URL}/models`;

// Listed first so it is pi's default even when goophy's list omits it.
const DEFAULT_MODEL = "{{DEFAULT_MODEL}}";

// Output caps /v1/models does not carry. Others get outputCap().
const MAX_TOKENS: Record<string, number> = { "nhn-coder": 131072 };

// Used only when goophy cannot be reached at startup.
const FALLBACK: GatewayModel[] = [{ id: "nhn-coder", max_model_len: 262144 }, { id: "nhn-medium" }, { id: "nhn-large" }];

interface GatewayModel {
  id: string;
  max_model_len?: number;
}

// A quarter of the context, at most 32k, so long prompts still fit.
function outputCap(contextWindow: number) {
  return Math.min(Math.floor(contextWindow / 4), 32768);
}

function toModel(m: GatewayModel) {
  const contextWindow = m.max_model_len ?? 131072;
  return {
    id: m.id,
    name: m.id,
    reasoning: false,
    input: ["text"] as ("text" | "image")[],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow,
    maxTokens: MAX_TOKENS[m.id] ?? outputCap(contextWindow),
  };
}

function isChatModel(id: string) {
  return !/(embedding|reranker|embed-)/i.test(id);
}

async function fetchModels(signal: AbortSignal): Promise<GatewayModel[]> {
  const res = await fetch(MODELS_URL, {
    signal: AbortSignal.any([signal, AbortSignal.timeout(5000)]),
  });
  if (!res.ok) throw new Error(`GET /v1/models -> ${res.status}`);
  const json = (await res.json()) as { data: GatewayModel[] };
  const models = json.data.filter((m) => isChatModel(m.id));
  if (models.length === 0) throw new Error("goophy listed no chat models");
  return models;
}

// Returns a new list with the default model first, adding it if missing.
function pinDefault(models: GatewayModel[]): GatewayModel[] {
  const def = models.find((m) => m.id === DEFAULT_MODEL) ?? { id: DEFAULT_MODEL };
  return [def, ...models.filter((m) => m.id !== DEFAULT_MODEL)];
}

export default async function (pi: ExtensionAPI) {
  // pi calls refreshModels only on a catalog refresh, not at startup, so the
  // factory supplies the startup list itself.
  let startup: GatewayModel[];
  try {
    startup = await fetchModels(new AbortController().signal);
  } catch (err) {
    console.warn(`goophy: could not list models (${(err as Error).message}); using a default list`);
    startup = FALLBACK;
  }

  pi.registerProvider("goophy", {
    name: "Goophy (NHN gateway)",
    baseUrl: BASE_URL,
    apiKey: "goophy",
    api: "openai-completions",
    models: pinDefault(startup).map(toModel),
    // pi replaces the list with what this returns, so a failed or cancelled
    // refresh throws instead of swapping the live list for the fallback.
    async refreshModels({ signal }) {
      const models = await fetchModels(signal);
      signal.throwIfAborted();
      return pinDefault(models).map(toModel);
    },
  });
}
