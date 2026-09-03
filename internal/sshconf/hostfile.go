package sshconf

// File is the parsed file this block was read from — the one a write to
// the block would go through, and the one an editor should open to reach
// it. Every Host returned by Hosts, Host, and IncludeHosts carries it.
func (h *Host) File() *File { return h.f }
