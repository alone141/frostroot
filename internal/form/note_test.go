package form

import "testing"

func TestNoteFieldAsksNothing(t *testing.T) {
	note := NoteField(PageCaptured, "What capture found", "Read /: Ubuntu 24.04\n\nA recipe cannot carry:\n  snap packages  2")
	if note.Kind != KindNote || note.Page != PageCaptured || note.Title != "What capture found" || note.Validate != nil || note.Options != nil {
		t.Errorf("NoteField = %+v; want a note on the Captured page with no validator and no options", note)
	}
	if pages := Pages(); pages[0] != PageCaptured {
		t.Errorf("pages = %v; want Captured first, so the note comes before every question", pages)
	}
	// The standard fields never use the page, so init and edit never show it.
	for _, field := range Fields(Host{}) {
		if field.Page == PageCaptured || field.Kind == KindNote {
			t.Errorf("a standard field is on the Captured page or is a note: %+v", field)
		}
	}
}
