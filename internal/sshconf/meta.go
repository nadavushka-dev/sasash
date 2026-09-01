package sshconf

import "strings"

// This file scopes metadata to the block that owns it. Meta() collects every
// meta line in [Start+1, End), but a block's End is simply the next Host
// line — so the comment run introducing the NEXT host also sits inside the
// range, and both blocks would claim it. Attachment must be exclusive: a meta
// line describes exactly one host.

// OwnMeta is Meta() minus the trailing unbroken comment run that leads into
// a following Host line: by the attachment rule those lines belong to the
// next block, not to this one. A run leading into a Match line stays here —
// no following block will claim it, and dropping it would silently lose data.
//
// The run boundary mirrors Meta()'s upward walk exactly: meta and plain
// comment lines continue the run, a blank line or directive breaks it.
func (h *Host) OwnMeta() []Meta {
	all := h.Meta()
	if h.End >= len(h.f.lines) {
		return all // last block in the file: nothing follows to claim the run
	}
	if !strings.EqualFold(parseLine(h.End, h.f.lines[h.End]).Keyword, "host") {
		return all // the run leads into a Match line, which owns no meta
	}

	boundary := h.End
	for boundary > h.Start+1 {
		switch parseLine(boundary-1, h.f.lines[boundary-1]).Kind {
		case KindMeta, KindComment:
			boundary--
			continue
		}
		break
	}
	if boundary == h.End {
		return all
	}

	var out []Meta
	for _, m := range all {
		if m.Index >= boundary && m.Index < h.End {
			continue
		}
		out = append(out, m)
	}
	return out
}

// OwnMetaVerb returns every OwnMeta entry with the given verb.
func (h *Host) OwnMetaVerb(verb string) []Meta {
	var out []Meta
	for _, m := range h.OwnMeta() {
		if strings.EqualFold(m.Verb, verb) {
			out = append(out, m)
		}
	}
	return out
}
