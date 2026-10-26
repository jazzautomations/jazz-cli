# IA grátis infinita

O jazz junta todos os planos grátis que você tiver e troca de provedor sozinho quando um
esgota. Uma chave por provedor; quanto mais chaves, mais fôlego. Sem nenhuma chave você
ainda tem o `kilo` (modelos `:free`, sem cadastro), mas ele tem cota e pode parar
(`PROMOTION_MODEL_LIMIT_REACHED`): é por isso que vale juntar outros.

Verificado em 26/10/2026 nas páginas de documentação citadas. Limite que a doc não publica
fica como "?" (não inventamos). Planos grátis mudam: confira a fonte antes de depender.

## Comece

```
jazz omni setup           # mostra cada provedor, o link do cadastro e deixa colar a chave
jazz omni setup --list    # só lista (sem perguntar nada)
jazz key <provedor>       # colar uma chave direto (ex.: jazz key groq)
jazz omni status          # quem está pronto, quem esfriou e quando volta
```

As chaves ficam no armazenamento de credenciais do jazz (`jazz key` / `jazz omni setup`) ou
nas variáveis de ambiente (ex.: `GROQ_API_KEY`). `jazz login` é só para o ChatGPT
(assinatura), não para chaves de API.

## Provedores

| Provedor | Cadastro | O que vem de graça (fonte, 26/10/2026) | Modelos por faixa (padrão do jazz) | Chave |
|---|---|---|---|---|
| kilo | https://app.kilo.ai | Sem chave: modelos `:free`, ~200 pedidos/hora por IP (valor do preset, **não confirmado** na doc lida: kilo.ai/docs/gateway) | coder `poolside/laguna-s-2.1:free`; fast `minimax/minimax-m3.1-flash:free` | não precisa |
| openrouter | https://openrouter.ai/settings/keys | Modelos `:free`: 20/min; 50/dia (menos de US$10 comprados) ou 1000/dia — https://openrouter.ai/docs/api-reference/limits | smart/coder `nvidia/nemotron-3-ultra-550b-a55b:free` | `jazz key openrouter` |
| gemini | https://aistudio.google.com/apikey | Plano grátis existe; **números não publicados** na doc (veja aistudio.google.com/rate-limit); RPD zera à meia-noite do Pacífico — https://ai.google.dev/gemini-api/docs/rate-limits. O Google pode usar os dados para treino | smart/coder `gemini-3.8-flash`; fast `gemini-3.5-flash-lite` | `jazz key gemini` |
| groq | https://console.groq.com/keys | `openai/gpt-oss-120b`, `gpt-oss-20b`, `qwen/qwen3.8-27b`: 30/min, 1000/dia, 8k tokens/min, 200k tokens/dia (tabela "Developer plan"; a aba do plano grátis não pôde ser lida: **incerto**) — https://console.groq.com/docs/rate-limits | fast `openai/gpt-oss-120b` | `jazz key groq` |
| cerebras | https://cloud.cerebras.ai | **Teste, não recorrente**: US$5 de crédito que expira em 30 dias; `gpt-oss-120b` e `qwen-3.8-27b`: 5/min, 30k tokens/min, 1M tokens/dia — https://inference-docs.cerebras.ai/support/rate-limits | fast `gpt-oss-120b` | `jazz key cerebras` |
| sambanova | https://cloud.sambanova.ai/apis | Sem cartão vinculado: 20/min, 20/dia, 200k tokens/dia por modelo (`DeepSeek-V3.1`, `Meta-Llama-3.3-70B-Instruct`, `gpt-oss-120b`) — https://docs.sambanova.ai/docs/en/models/rate-limits | coder `DeepSeek-V3.1`; fast `gpt-oss-120b` | `jazz key sambanova` |
| cloudflare | https://dash.cloudflare.com/profile/api-tokens | 10 mil neurons/dia, zera 00:00 UTC — https://developers.cloudflare.com/workers-ai/platform/pricing/ . Precisa também do Account ID (`CLOUDFLARE_ACCOUNT_ID`) | coder `@cf/moonshotai/kimi-k2.5` | `jazz key cloudflare` |
| nvidia | https://build.nvidia.com/settings/api-keys | Catálogo grátis; limite por minuto **não publicado** na doc lida (preset assume ~40/min, não verificado). A NVIDIA registra o uso | coder/smart `z-ai/glm-5.3`, `moonshotai/kimi-k2.6` | `jazz key nvidia` |
| mistral | https://console.mistral.ai/api-keys | Plano Experiment (telefone; aceita treino com seus dados). Limites **não publicados** na doc lida | coder `devstral-medium-latest` | `jazz key mistral` |
| codestral | https://console.mistral.ai/codestral | Chave própria do Codestral; limites não verificados (preset: 30/min, 2000/dia). Bom para completar código, fraco como agente | fast `codestral-latest` | `jazz key codestral` |
| ollama-cloud | https://ollama.com/settings/keys | Plano grátis com cota pequena; a doc remete a ollama.com/settings/usage (**números não publicados**) — https://docs.ollama.com/cloud | coder `glm-5.3-flash` | `jazz key ollama-cloud` |
| zai | https://z.ai/manage-apikey/apikey-list | GLM-4.7-Flash grátis, 1 pedido por vez (preset, não verificado agora) | coder `glm-4.7-flash` | `jazz key zai` |
| cohere | https://dashboard.cohere.com/api-keys | Chave trial, ~1000 chamadas/mês, só avaliação (preset, não verificado agora) | coder `north-mini-code` | `jazz key cohere` |

### Não entram na rotação grátis

- **GitHub Models**: encerrado em 30/07/2026 (playground, catálogo e API) — https://docs.github.com/en/github-models . Por isso não existe "usar o `gh`": não há endpoint para chamar.
- **Hugging Face Inference Providers** (`https://router.huggingface.co/v1`, token `hf_…`): contas grátis **não têm crédito mensal**; PRO recebe US$2/mês — https://huggingface.co/docs/inference-providers/pricing . O jazz traz o provedor (`jazz key huggingface`), mas ele conta como pago e só é usado em emergência (`omni.paid`).
- Chaves pagas (deepseek, openai, anthropic) e o ChatGPT por assinatura (`jazz login chatgpt`) ficam no fim da fila (`omni.paid = emergency`).

## Como a rotação funciona

1. A cadeia ordena os candidatos grátis por qualidade e fôlego restante e tenta o primeiro.
2. **429 de cota** (a mensagem diz que a cota/promoção grátis acabou, ex.: `PROMOTION_MODEL_LIMIT_REACHED`, `quota exceeded`): o provedor inteiro esfria até o horário que o erro informar (`Retry-After`, `X-RateLimit-Reset*`, ou um "resets in …"/`resets_at` no corpo) ou **15 min** se não disser nada, e o jazz passa para o próximo candidato **de outro provedor**.
3. **429 de ritmo** (por minuto): espera curta e tenta de novo; se persistir, esfria um pouco mais a cada tentativa.
4. **401/403**: a chave é marcada como recusada até você trocá-la.
5. Cota diária/mensal já gasta (contador local ou cabeçalhos) tira o modelo até o reset.
6. 429 de cota **não penaliza** o provedor no placar do juiz (não é qualidade).
7. Quando todo provedor grátis com chave está esfriando, `jazz omni status` mostra `pool SECO`, quando o primeiro volta, e o caminho mais rápido: `jazz omni setup`.

`jazz omni status` lista por provedor: origem da chave, estado (`ok`, `429 cota`, `401`, …), "volta em",
último sucesso, uso (hoje/mês) e os limites da doc. `jazz omni reset [provedor]` zera os contadores.
