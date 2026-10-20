package main

import "testing"

func TestParseMarkdown(t *testing.T) {
	md := "# Top\n\n## Flow\n\n```mermaid\nflowchart LR\n  A --> B\n```\n\n````md\n```mermaid\nnot a diagram\n```\n````\n\n~~~ mermaid\nsequenceDiagram\n  A->>B: hi\n~~~\n"
	got := parseFile("x.md", []byte(md))
	if len(got) != 2 {
		t.Fatalf("got %d diagrams, want 2: %+v", len(got), got)
	}
	if got[0].Title != "Flow" || got[0].Line != 6 || got[0].Src != "flowchart LR\n  A --> B" {
		t.Errorf("first diagram = %+v", got[0])
	}
	if got[1].Line != 17 || got[1].Src != "sequenceDiagram\n  A->>B: hi" {
		t.Errorf("second diagram = %+v", got[1])
	}
}

func TestParseMmd(t *testing.T) {
	got := parseFile("x.mmd", []byte("graph TD\n  A-->B\n"))
	if len(got) != 1 || got[0].Line != 1 || got[0].Src != "graph TD\n  A-->B\n" {
		t.Errorf("got %+v", got)
	}
}
