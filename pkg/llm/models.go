package llm

type Node struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Provenance  string  `json:"provenance"`
	ParentID    string  `json:"parent_id,omitempty"`
}

type Insight struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Provenance string `json:"provenance"`
}

type Source struct {
	Title      string `json:"title"`
	Type       string `json:"type"`
	Confidence string `json:"confidence"`
	Body       string `json:"body"`
	Provenance string `json:"provenance"`
	URL        string `json:"url"`
}

type Tradeoff struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Provenance string `json:"provenance"`
}

type Citation struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Snippet    string `json:"snippet"`
	Page       *int   `json:"page,omitempty"`
	Provenance string `json:"provenance"`
}

type ResearchPayload struct {
	Degraded   bool       `json:"-"`
	ID         string     `json:"id,omitempty"`
	SessionID  string     `json:"session_id,omitempty"`
	Topic      string     `json:"topic,omitempty"`
	Depth      string     `json:"depth,omitempty"`
	Output     string     `json:"output,omitempty"`
	Root       string     `json:"root,omitempty"`
	Summary    string     `json:"summary"`
	Nodes      []Node     `json:"nodes"`
	Insights   []Insight  `json:"insights"`
	Sources    []Source   `json:"sources"`
	Tradeoffs  []Tradeoff `json:"tradeoffs"`
	Citations  []Citation `json:"citations"`
	Warnings   []string   `json:"warnings"`
	Provenance string     `json:"provenance,omitempty"`
}

type QAPayload struct {
	Degraded           bool       `json:"-"`
	Answer             string     `json:"answer"`
	Provenance         string     `json:"provenance"`
	Citations          []Citation `json:"citations"`
	EvidenceSufficient bool       `json:"evidence_sufficient"`
	Warnings           []string   `json:"warnings"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
