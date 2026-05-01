import json
import os
import re
from typing import Dict, List

import requests


GROQ_URL = "https://api.groq.com/openai/v1/chat/completions"
OPENROUTER_URL = "https://openrouter.ai/api/v1/chat/completions"


def call_groq(messages: List[Dict], use_search: bool = False, timeout: int = 8) -> Dict:
    api_key = os.getenv("GROQ_API_KEY")
    model = os.getenv("GROQ_MODEL", "llama-3.1-8b-instant")
    if not api_key:
        return {"offline": True, "content": ""}

    try:
        response = requests.post(
            GROQ_URL,
            timeout=timeout,
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
            },
            json={
                "model": model,
                "messages": messages,
                "temperature": 0.2,
                "response_format": {"type": "json_object"},
            },
        )
        response.raise_for_status()
    except requests.RequestException as exc:
        status = getattr(getattr(exc, "response", None), "status_code", None)
        message = f"Groq request failed{f' with HTTP {status}' if status else ''}; returned local fallback output."
        return {"offline": True, "content": "", "warning": message}
    payload = response.json()
    choice = payload["choices"][0]["message"]
    return {
        "offline": False,
        "content": choice.get("content", ""),
        "executed_tools": choice.get("executed_tools") or [],
        "raw": payload,
    }


def call_gemini(system_prompt: str, user_prompt: str, timeout: int = 8) -> Dict:
    api_key = os.getenv("GEMINI_API_KEY")
    if not api_key:
        return {"offline": True, "content": "", "warning": "GEMINI_API_KEY not set."}

    url = f"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key={api_key}"
    payload = {
        "systemInstruction": {
            "parts": [{"text": system_prompt}]
        },
        "contents": [
            {"role": "user", "parts": [{"text": user_prompt}]}
        ],
        "generationConfig": {
            "responseMimeType": "application/json",
            "temperature": 0.2
        }
    }

    try:
        resp = requests.post(url, json=payload, timeout=timeout)
        resp.raise_for_status()
        data = resp.json()
        content = data["candidates"][0]["content"]["parts"][0]["text"]
        return {"offline": False, "content": content}
    except Exception as exc:
        status = getattr(getattr(exc, "response", None), "status_code", None)
        return {"offline": True, "content": "", "warning": f"Gemini request failed (HTTP {status})"}


def call_duckduckgo(query: str, max_results: int = 5) -> List[Dict]:
    from duckduckgo_search import DDGS
    try:
        results = list(DDGS().text(query, max_results=max_results))
        return [
            {"title": r.get("title", ""), "body": r.get("body", ""), "url": r.get("href", "")}
            for r in results
        ]
    except Exception:
        return []


def call_openrouter(messages: List[Dict], timeout: int = 8) -> Dict:
    api_key = os.getenv("OPENROUTER_API_KEY")
    if not api_key:
        return {"offline": True, "content": "", "warning": "OPENROUTER_API_KEY not set."}
    try:
        resp = requests.post(
            OPENROUTER_URL,
            timeout=timeout,
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
            },
            json={
                "model": "mistralai/mistral-7b-instruct",
                "messages": messages,
                "temperature": 0.2,
            },
        )
        resp.raise_for_status()
        data = resp.json()
        content = data["choices"][0]["message"]["content"]
        return {"offline": False, "content": content}
    except Exception as exc:
        status = getattr(getattr(exc, "response", None), "status_code", None)
        return {"offline": True, "content": "", "warning": f"OpenRouter request failed (HTTP {status})"}


def call_llm_with_fallback(
    messages: List[Dict],
    use_search: bool = False,
    llm_timeout: int = 8,
    skip_ddg: bool = False,
    max_llm_stages: int = 3,
) -> Dict:
    # 1. Groq
    result = call_groq(messages, use_search, timeout=llm_timeout)
    if not result.get("offline"):
        return result

    system_prompt = next((m["content"] for m in messages if m["role"] == "system"), "")
    user_prompt = next((m["content"] for m in messages if m["role"] == "user"), "")

    # 2. Gemini
    gemini_result = call_gemini(system_prompt, user_prompt, timeout=llm_timeout)
    if not gemini_result.get("offline"):
        gemini_result["fallback_warning"] = "Groq unavailable. Used Gemini fallback."
        return gemini_result

    # Callers can cap at 2 stages (Groq + Gemini) to stay within tight serverless
    # timeouts. Q&A uses max_llm_stages=2: 4s + 4s = 8s max, leaving headroom
    # for FastAPI/Vercel overhead under the 10s function limit.
    if max_llm_stages <= 2:
        return gemini_result

    # 3. OpenRouter
    openrouter_result = call_openrouter(messages, timeout=llm_timeout)
    if not openrouter_result.get("offline"):
        openrouter_result["fallback_warning"] = "Groq and Gemini unavailable. Used OpenRouter fallback."
        return openrouter_result

    # 4. DuckDuckGo RAG — retrieve then re-synthesise with an LLM
    # Skipped for document Q&A (private evidence; DDG can't help) and when
    # caller needs to stay within tight serverless timeouts.
    if not skip_ddg:
        query = f"{user_prompt}"[:300]
        ddg_results = call_duckduckgo(query)
        if ddg_results:
            search_context = "\n\n".join(
                f"{r['title']}: {r['body']}" for r in ddg_results
            )
            rag_messages = list(messages) + [
                {"role": "system", "content": "Use the following web results to answer the question."},
                {"role": "user", "content": search_context},
            ]

            rag_gemini = call_gemini(
                system_prompt,
                f"{user_prompt}\n\nWeb results:\n{search_context}",
                timeout=llm_timeout,
            )
            if not rag_gemini.get("offline"):
                rag_gemini["fallback_warning"] = "All primary LLMs failed. Used DuckDuckGo + Gemini RAG fallback."
                return rag_gemini

            rag_openrouter = call_openrouter(rag_messages, timeout=llm_timeout)
            if not rag_openrouter.get("offline"):
                rag_openrouter["fallback_warning"] = "All primary LLMs failed. Used DuckDuckGo + OpenRouter RAG fallback."
                return rag_openrouter

    # 5. Extractive fallback
    return result


# ── Depth profiles ──────────────────────────────────────────────────────────
_DEPTH_PROFILES = {
    "Basic": {
        "node_count": "exactly 4 nodes",
        "insight_count": "2–3 insights",
        "tradeoff_count": "1–2 trade-offs",
        "language": "Use plain, jargon-free language suitable for a general audience.",
        "summary_length": "Write a 1–2 sentence high-level overview.",
    },
    "Detailed": {
        "node_count": "6–8 nodes",
        "insight_count": "4–5 insights",
        "tradeoff_count": "2–3 trade-offs",
        "language": "Use clear, moderately technical language with defined terms where needed.",
        "summary_length": "Write a 3–4 sentence balanced overview covering key themes.",
    },
    "Expert": {
        "node_count": "10–12 nodes",
        "insight_count": "6–8 insights",
        "tradeoff_count": "4–5 trade-offs",
        "language": "Use precise technical terminology. Assume the reader has deep domain expertise.",
        "summary_length": "Write a 5–6 sentence analytical summary covering nuances, open debates, and research gaps.",
    },
}

# ── Output format profiles ───────────────────────────────────────────────────
_OUTPUT_PROFILES = {
    "Mindmap": (
        "Structure output as an interconnected concept graph. "
        "Nodes should represent distinct sub-concepts with descriptive titles. "
        "Insights highlight cross-cutting themes. "
        "Tradeoffs capture conceptual tensions between nodes."
    ),
    "Report": (
        "Structure output as a formal research report. "
        "The summary should read as an executive introduction. "
        "Nodes represent main sections (e.g., Background, Findings, Methodology, Implications, Gaps). "
        "Insights should be structured as numbered findings. "
        "Tradeoffs should frame competing schools of thought or methodological debates."
    ),
    "Comparison": (
        "Structure output as a comparative analysis. "
        "Each node should represent one distinct approach, technology, framework, or perspective being compared. "
        "Insights should explicitly contrast two or more options (use 'vs.' phrasing). "
        "Tradeoffs must contain concrete pros and cons for each compared option. "
        "Sources should prioritize comparative studies and benchmarks."
    ),
    "Bullets": (
        "Structure output for fast scanning. "
        "The summary must be a single string containing 3–5 punchy bullet points separated by newline characters (\\n) — NOT a JSON array, always a string. "
        "Node descriptions should be single-sentence takeaways. "
        "Insights should each contain one key fact or statistic. "
        "Tradeoffs should be formatted as 'Pro: ... / Con: ...' pairs."
    ),
}

# ── Source mix profiles ──────────────────────────────────────────────────────
_SOURCE_PROFILES = {
    "Academic + web": (
        "Prioritise peer-reviewed papers, academic databases (arXiv, PubMed, IEEE, ACM), "
        "and high-authority web sources (government, universities, established journals). "
        "Citations must include DOI or canonical URL where possible."
    ),
    "Market signals": (
        "Prioritise industry reports (Gartner, McKinsey, Forrester, CB Insights), "
        "press releases, earnings calls, startup funding data, and trend analyses. "
        "Frame insights around business impact, market size, growth rate, and competitive dynamics. "
        "Tradeoffs should reflect business risks and opportunities."
    ),
    "Internal notes": (
        "Draw exclusively from the provided evidence. Do NOT invent external sources. "
        "Every node, insight, and citation must be traceable to the supplied evidence text. "
        "If evidence is sparse, explicitly flag gaps rather than filling them with speculation. "
        "Set provenance to 'source_grounded' for all items."
    ),
}


def build_mindmap_prompt(
    topic: str,
    depth: str,
    output: str,
    source: str = "Academic + web",
    evidence: str = "",
    web_search: bool = False,
) -> List[Dict]:
    provenance = "web_enriched" if web_search else "source_grounded"
    source_block = evidence[:12000] if evidence else "No local evidence was provided."

    dp = _DEPTH_PROFILES.get(depth, _DEPTH_PROFILES["Detailed"])
    op = _OUTPUT_PROFILES.get(output, _OUTPUT_PROFILES["Mindmap"])
    sp = _SOURCE_PROFILES.get(source, _SOURCE_PROFILES["Academic + web"])

    system_prompt = (
        "Return only valid JSON. You are a structured research synthesis engine. "
        "Every insight, node, tradeoff, source, and citation must include a provenance field "
        "whose value is exactly one of: 'source_grounded', 'ai_synthesized', or 'web_enriched'."
    )

    user_prompt = f"""Topic: {topic}

=== DEPTH: {depth} ===
- Produce {dp['node_count']}.
- Produce {dp['insight_count']}.
- Produce {dp['tradeoff_count']}.
- {dp['language']}
- {dp['summary_length']}

=== OUTPUT FORMAT: {output} ===
{op}

=== SOURCE MIX: {source} ===
{sp}

=== PROVIDED EVIDENCE ===
{source_block}

=== INSTRUCTIONS ===
Default provenance for AI-generated content: {provenance}
Return JSON with exactly these top-level keys:
  summary       : string
  nodes         : array of objects — title (string), description (string), x (number 0–90), y (number 0–90), provenance (string)
  insights      : array of objects — title (string), body (string), provenance (string)
  sources       : array of objects — title (string), type (string), confidence (string: "High"|"Medium"|"Low"), body (string), provenance (string), url (string)
  tradeoffs     : array of objects — title (string), body (string), provenance (string)
  citations     : array of objects — title (string), url (string), snippet (string), provenance (string)

Do not include any markdown, code fences, or explanatory text outside the JSON object.
"""

    return [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": user_prompt},
    ]


def parse_json_or_fallback(content: str, fallback: Dict, required_keys: List[str] = None) -> Dict:
    if required_keys is None:
        required_keys = ["summary"]

    if not content:
        return fallback
        
    def has_required_keys(parsed_obj: Dict) -> bool:
        return isinstance(parsed_obj, dict) and all(k in parsed_obj for k in required_keys)
        
    try:
        import json_repair
        parsed = json_repair.loads(content)
        if has_required_keys(parsed):
            return parsed
    except Exception:
        pass
        
    try:
        return json.loads(content)
    except json.JSONDecodeError:
        match = re.search(r"\{.*\}", content, re.S)
        if match:
            try:
                import json_repair
                parsed = json_repair.loads(match.group(0))
                if has_required_keys(parsed):
                    return parsed
            except Exception:
                pass
            try:
                return json.loads(match.group(0))
            except json.JSONDecodeError:
                pass
                
    if "summary" in fallback:
        fallback["summary"] = content[:900] if content else fallback.get("summary", "")
    elif "answer" in fallback:
        # Avoid storing raw JSON in answer if it failed to parse properly
        fallback["answer"] = content[:900] if content else fallback.get("answer", "")
        
    return fallback


def offline_generation(topic: str, evidence_chunks=None, provenance: str = "ai_synthesized") -> Dict:
    evidence_chunks = evidence_chunks or []
    source_titles = sorted({chunk.get("title", "Provided evidence") for chunk in evidence_chunks})[:4]
    summary = (
        f"Research map for {topic}. "
        "Set GROQ_API_KEY to enable Groq Compound search and higher quality synthesis."
    )
    if evidence_chunks:
        summary = f"Evidence-backed research map for {topic} using {len(evidence_chunks)} local evidence chunks."

    nodes = [
        {"title": "Context", "description": "Frame the core question and constraints.", "x": 8, "y": 11, "provenance": provenance},
        {"title": "Evidence", "description": "Use provided material before web expansion.", "x": 70, "y": 12, "provenance": "source_grounded" if evidence_chunks else provenance},
        {"title": "Patterns", "description": "Cluster recurring concepts and claims.", "x": 8, "y": 58, "provenance": provenance},
        {"title": "Trade-offs", "description": "Separate tensions, risks, and open questions.", "x": 70, "y": 58, "provenance": provenance},
        {"title": "Next research", "description": "Identify gaps and follow-up searches.", "x": 39, "y": 78, "provenance": provenance},
    ]
    return {
        "summary": summary,
        "nodes": nodes,
        "insights": [
            {"title": "Evidence-first workflow", "body": "Local sources should answer questions before web search is used.", "provenance": "source_grounded" if evidence_chunks else provenance},
            {"title": "Traceable synthesis", "body": "Generated claims are labeled separately from source-backed material.", "provenance": "ai_synthesized"},
            {"title": "Research gaps stay visible", "body": "The map keeps unanswered areas available for follow-up questions.", "provenance": provenance},
        ],
        "sources": [
            {"title": title, "type": "Provided evidence", "confidence": "High", "body": "Stored local evidence source.", "provenance": "source_grounded", "url": ""}
            for title in source_titles
        ],
        "tradeoffs": [
            {"title": "Coverage vs. cost", "body": "The system limits token usage by searching local evidence before web search.", "provenance": "ai_synthesized"},
            {"title": "Speed vs. nuance", "body": "Fast summaries need citations and provenance labels to remain trustworthy.", "provenance": "ai_synthesized"},
        ],
        "citations": [
            {"title": chunk.get("title", "Evidence"), "url": chunk.get("url") or "", "snippet": chunk.get("text", "")[:220], "provenance": "source_grounded"}
            for chunk in evidence_chunks[:6]
        ],
        "warnings": ["GROQ_API_KEY is not set; returned local fallback output."],
    }


def generate_research(
    topic: str,
    depth: str,
    output: str,
    evidence_chunks=None,
    source: str = "Academic + web",
    web_search: bool = False,
) -> Dict:
    evidence_chunks = evidence_chunks or []
    evidence_text = "\n\n".join(
        f"[{idx + 1}] {chunk.get('title')} ({chunk.get('source_type')}): {chunk.get('text')}"
        for idx, chunk in enumerate(evidence_chunks[:10])
    )
    fallback = offline_generation(topic, evidence_chunks, "web_enriched" if web_search else "source_grounded")

    data = None
    result = None
    for attempt in range(2):
        result = call_llm_with_fallback(
            build_mindmap_prompt(topic, depth, output, source, evidence_text, web_search),
            use_search=web_search,
        )
        if result.get("offline"):
            break

        data = parse_json_or_fallback(result.get("content", ""), fallback, required_keys=["summary"])
        if data is not fallback:
            break

    if result.get("offline") or data is fallback:
        if result.get("offline"):
            fallback.setdefault("warnings", []).append(result.get("warning", "All LLM APIs unavailable; returned local fallback output."))
        else:
            fallback.setdefault("warnings", []).append("Models returned invalid JSON repeatedly. Showing fallback.")
        return normalize_payload(fallback)
        
    data.setdefault("warnings", [])
    if "fallback_warning" in result:
        data["warnings"].append(result["fallback_warning"])

    for tool in result.get("executed_tools", []):
        for search_result in tool.get("search_results", []) or []:
            if isinstance(search_result, str):
                data.setdefault("citations", []).append(
                    {
                        "title": "Web result",
                        "url": "",
                        "snippet": search_result[:260],
                        "provenance": "web_enriched",
                    }
                )
                continue
            data.setdefault("citations", []).append(
                {
                    "title": search_result.get("title", "Web result"),
                    "url": search_result.get("url", ""),
                    "snippet": search_result.get("content", "")[:260],
                    "provenance": "web_enriched",
                }
            )
    return normalize_payload(data)


def answer_question(question: str, evidence_chunks: List[Dict], use_web: bool) -> Dict:
    evidence_text = "\n\n".join(
        f"[{idx + 1}] {chunk.get('title')}: {chunk.get('text')}"
        for idx, chunk in enumerate(evidence_chunks[:8])
    )
    if not os.getenv("GROQ_API_KEY"):
        if evidence_chunks:
            answer = f"Based on the provided evidence, the closest answer is: {evidence_chunks[0]['text'][:500]}"
            provenance = "source_grounded"
        else:
            answer = "Set GROQ_API_KEY to answer questions that require web search or LLM synthesis."
            provenance = "ai_synthesized"
        return normalize_payload({
            "answer": answer,
            "provenance": provenance,
            "citations": [
                {"title": chunk.get("title"), "url": chunk.get("url") or "", "snippet": chunk.get("text", "")[:220], "provenance": "source_grounded"}
                for chunk in evidence_chunks[:5]
            ],
            "warnings": ["GROQ_API_KEY is not set; returned local fallback answer."],
        })

    mode = "Use the evidence first. If insufficient, use web search." if use_web else "Use only the provided evidence."
    result = call_llm_with_fallback(
        [
            {"role": "system", "content": "Return only valid JSON with keys answer, provenance, citations, evidence_sufficient."},
            {
                "role": "user",
                "content": f"""
Question: {question}
Mode: {mode}

Evidence:
{evidence_text}

Return citations as objects with title, url, snippet, provenance. Output must be in JSON format.
""",
            },
        ],
        use_search=use_web,
        # Keep Q&A within Vercel's 10s function limit.
        # max_llm_stages=2 → only Groq (4s) + Gemini (4s) = 8s max; OpenRouter
        # and DDG are skipped so we always have ~2s of headroom for overhead.
        llm_timeout=4,
        skip_ddg=True,
        max_llm_stages=2,
    )
    fallback = {
        "answer": result.get("content", ""),
        "provenance": "web_enriched" if use_web else "source_grounded",
        "citations": [],
        "evidence_sufficient": bool(evidence_chunks),
    }
    if result.get("offline"):
        if evidence_chunks:
            fallback["answer"] = f"Based on the provided evidence, the closest answer is: {evidence_chunks[0]['text'][:500]}"
            fallback["provenance"] = "source_grounded"
            fallback["citations"] = [
                {"title": chunk.get("title"), "url": chunk.get("url") or "", "snippet": chunk.get("text", "")[:220], "provenance": "source_grounded"}
                for chunk in evidence_chunks[:5]
            ]
        fallback["warnings"] = [result.get("warning", "All LLM APIs unavailable; returned local fallback answer.")]
        return normalize_payload(fallback)
        
    parsed_data = parse_json_or_fallback(result.get("content", ""), fallback, required_keys=["answer"])
    if "fallback_warning" in result:
        parsed_data.setdefault("warnings", []).append(result["fallback_warning"])
    return normalize_payload(parsed_data)


def normalize_payload(data: Dict) -> Dict:
    valid = {"source_grounded", "ai_synthesized", "web_enriched"}

    def clean(value, default="ai_synthesized"):
        return value if value in valid else default

    data["provenance"] = clean(data.get("provenance"))
    for key in ["nodes", "insights", "sources", "tradeoffs", "citations"]:
        items = data.get(key)
        if isinstance(items, list):
            for item in items:
                if isinstance(item, dict):
                    item["provenance"] = clean(item.get("provenance"), "source_grounded" if key == "citations" else "ai_synthesized")
                    if key == "sources" and not isinstance(item.get("confidence", ""), str):
                        item["confidence"] = str(item.get("confidence"))
    return data
