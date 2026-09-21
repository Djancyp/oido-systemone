# API

## `POST /v1/systemone`

Body: `{ "model", "state", "questions" }`

- `model`: picks the model. `oido-rlhf-minicpm5-2b`, `oido-rlhf-qwen3.5-4b`, `oido-rlhf-qwen3-4b` (if loaded), or `jev-latest` / `jev-preview` for the default (first in `MODEL`). The response `model` field says which one answered. Unknown or not-loaded id: `422` listing valid ones
- `state`: string, object or array. The content every question refers to
- `questions`: object of `name -> question`, 1 to 64 questions, answered in parallel

Question types (`type` field):

| type | Fields | Answer |
|------|--------|--------|
| `noul` (yes/no) | `instructions`, optional `criteria: {true, false}` | `{type, noul}` probability of yes, 0..1 |
| `choice` | `instructions`, `criteria: {name: description, ...}` (1 to 255 options) | `{type, choice, probabilities, confidence}` |
| `score` | `instructions`, `criteria: [level0, level1, ...]` (1 to 20 levels) | `{type, score, legend, probabilities, confidence}`, score is the probability-weighted level |

`instructions` and criteria values may be strings, objects or arrays. Choice option order = key
order in the request (it fixes the letters the model sees).

Response: `{ "model", "answers": {name: answer}, "usage": {input_tokens, output_tokens} }`.
Every response carries an `x-typesafe-request-id` header.

## Example

```sh
curl -s localhost:8080/v1/systemone \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "jev-latest",
    "state": {"subject": "Duplicate charge", "message": "I was charged twice. Please help."},
    "questions": {
      "billing": {"type": "noul", "instructions": "Is this message about billing?"},
      "tone": {
        "type": "choice",
        "instructions": "What is the tone of this message?",
        "criteria": {"angry": "Hostile or furious", "neutral": "Calm", "worried": "Anxious"}
      },
      "urgency": {
        "type": "score",
        "instructions": "How urgent is this message?",
        "criteria": ["Not urgent", "Somewhat urgent", "Very urgent"]
      }
    }
  }' | jq
```

With `API_KEY` set add `-H "Authorization: Bearer $API_KEY"`.

## Errors

| Status | When |
|--------|------|
| `403` | `API_KEY` set, no `Authorization: Bearer` header |
| `401` | Wrong key |
| `413` | Body over 4 MiB |
| `422` | Validation (FastAPI-style `{"detail": [{loc, msg, type}]}`). Also input too long for the context window |
| `429` | Client over `RATE_LIMIT` |
| `504` | Request passed `REQUEST_TIMEOUT`, or one forward pass passed 2 min |
| `529` | More than `-queue` requests in flight. `Retry-After: 1` |
| `500` | Model failure |

Auth and server errors use `{"detail": {"error_type", "message"}}`.

## Swagger (dev)

| URL | What |
|-----|------|
| `http://localhost:8080/docs` | Swagger UI ("Try it out", Authorize for Bearer key) |
| `http://localhost:8080/openapi.json` | OpenAPI 3.1 spec |

Swagger UI is loaded from the jsDelivr CDN, so `/docs` needs internet. The spec is embedded from
`openapi.json` (TypeSafe's published spec, `/v1/models` removed because it is not implemented).
`/docs` and `/openapi.json` are always on and never require the API key.

## How scoring works

1. State goes in the system message, so all questions on one state share a prefilled KV prefix.
2. Options are labelled `A`, `B`, ... The model's first-token top-20 logprobs are read; `A` and ` A` masses are summed; softmax gives probabilities.
3. Unless `BOTH_ORDERS=false`, each question is also run with reversed option order and averaged.
4. Choices with more than 20 options use two stages: pick a group of 16, then pick within the top 3 groups. Probabilities multiply, so they still sum to 1.
5. `confidence` rescales the top probability so uniform = 0, certain = 1.

## Known limits

- Kronk has no `logit_bias`. An option letter outside the top-20 gets the 20th logprob: an upper bound, its probability is overstated, never understated.
- State size is limited by a byte heuristic (3 bytes/token, 3/4 of the window). Past it: `422 too_long`.
- `-slots` above 1 does not help much on CPU.
- `/v1/models` from TypeSafe's API is not implemented.
