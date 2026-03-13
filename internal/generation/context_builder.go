package generation

import (
	"fmt"
	"strings"

	"github.com/neuco-ai/neuco/internal/domain"
)

// tokenEstimate returns a rough token count for a string using the common
// approximation of 1 token ≈ 4 characters.
func tokenEstimate(s string) int {
	return (len(s) + 3) / 4
}

// maxContextTokens is the total token budget allocated to few-shot codebase
// examples in the code generation prompt.
const maxContextTokens = 4000

// BuildCodegenContext selects the most relevant components and stories from the
// repo index and formats them as a text block suitable for inclusion in a code
// generation prompt. It stays within the maxContextTokens budget.
//
// Selection strategy:
//  1. Include design token / theme files first (they are small and give the LLM
//     styling context that is hard to infer from components alone).
//  2. Include story files — they illustrate expected component API surface and
//     use-case coverage.
//  3. Include component source files, preferring shorter ones so more examples
//     fit within the token budget.
//  4. Include type files for shared interfaces.
func BuildCodegenContext(index *RepoIndex, spec *domain.Spec) string {
	if index == nil {
		return ""
	}

	var sb strings.Builder
	remaining := maxContextTokens

	writeSection := func(header, path, content string) bool {
		block := fmt.Sprintf("### %s\n```\n// %s\n%s\n```\n\n", header, path, strings.TrimSpace(content))
		cost := tokenEstimate(block)
		if cost > remaining {
			return false
		}
		sb.WriteString(block)
		remaining -= cost
		return true
	}

	// --- 1. Framework / tooling preamble ---
	if index.Framework != "" || index.Styling != "" {
		preamble := "## Codebase Context\n\n"
		if index.Framework != "" {
			preamble += fmt.Sprintf("- Framework: %s\n", index.Framework)
		}
		if index.Styling != "" {
			preamble += fmt.Sprintf("- Styling: %s\n", index.Styling)
		}
		if index.TestSetup != "" {
			preamble += fmt.Sprintf("- Test setup: %s\n", index.TestSetup)
		}
		preamble += "\n"
		sb.WriteString(preamble)
		remaining -= tokenEstimate(preamble)
	}

	// --- 2. Design tokens (up to 2) ---
	for i, dt := range index.DesignTokens {
		if i >= 2 {
			break
		}
		writeSection("Design token: "+dt.Path, dt.Path, fmt.Sprintf("(size: %d bytes)", dt.FileSize))
	}

	// --- 3. Stories (up to 3) ---
	// Prefer stories whose component name appears in the spec text to maximise
	// relevance.
	specText := strings.ToLower(spec.ProposedSolution + " " + spec.UIChanges + " " + spec.ProblemStatement)
	var relevantStories []StoryInfo
	var otherStories []StoryInfo
	for _, s := range index.Stories {
		if strings.Contains(specText, strings.ToLower(s.ComponentName)) {
			relevantStories = append(relevantStories, s)
		} else {
			otherStories = append(otherStories, s)
		}
	}
	storyExamples := append(relevantStories, otherStories...)
	storiesAdded := 0
	for _, story := range storyExamples {
		if storiesAdded >= 3 {
			break
		}
		placeholder := fmt.Sprintf("(Storybook story for %s, %d bytes)", story.ComponentName, story.FileSize)
		if writeSection("Story example: "+story.Path, story.Path, placeholder) {
			storiesAdded++
		}
	}

	// --- 4. Component files (up to 5, shorter first) ---
	// Sort a copy of the components slice by file size ascending so smaller
	// components (which are often more focused) are included first.
	sorted := make([]ComponentInfo, len(index.Components))
	copy(sorted, index.Components)
	sortByFileSize(sorted)

	// Boost components whose name appears in the spec.
	var relevantComps []ComponentInfo
	var otherComps []ComponentInfo
	for _, c := range sorted {
		if strings.Contains(specText, strings.ToLower(c.Name)) {
			relevantComps = append(relevantComps, c)
		} else {
			otherComps = append(otherComps, c)
		}
	}
	orderedComps := append(relevantComps, otherComps...)

	compsAdded := 0
	for _, comp := range orderedComps {
		if compsAdded >= 5 {
			break
		}
		// Build a compact representation: props list + import list.
		var desc strings.Builder
		if len(comp.Props) > 0 {
			fmt.Fprintf(&desc, "// Props: %s\n", strings.Join(comp.Props, ", "))
		}
		if len(comp.Imports) > 0 {
			fmt.Fprintf(&desc, "// Imports: %s\n", strings.Join(comp.Imports, ", "))
		}
		fmt.Fprintf(&desc, "// File size: %d bytes", comp.FileSize)

		if writeSection("Component: "+comp.Name, comp.Path, desc.String()) {
			compsAdded++
		}
	}

	// --- 5. Type files (up to 2) ---
	for i, tf := range index.TypeFiles {
		if i >= 2 {
			break
		}
		writeSection("Type definitions: "+tf.Path, tf.Path, fmt.Sprintf("(size: %d bytes)", tf.FileSize))
	}

	result := sb.String()
	if strings.TrimSpace(result) == "" {
		return ""
	}
	return result
}

// sortByFileSize performs an in-place insertion sort of ComponentInfo by
// FileSize ascending. The slice is small (at most ~40 entries) so insertion
// sort is appropriate.
func sortByFileSize(comps []ComponentInfo) {
	for i := 1; i < len(comps); i++ {
		key := comps[i]
		j := i - 1
		for j >= 0 && comps[j].FileSize > key.FileSize {
			comps[j+1] = comps[j]
			j--
		}
		comps[j+1] = key
	}
}
