package omnigent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
)

// Files reaches a session's files.
//
// Every file route the server publishes is session-scoped, so this type's only
// job is to name the session. Upstream keeps a flat half — files.get(file_id) and
// friends — but every one of those raises: /v1/files was removed, and they survive
// as signposts to the session-scoped call. This package does not port a method
// that cannot work.
type Files struct {
	client *Client
}

// Files returns the file surface, bound to this client.
func (c *Client) Files() *Files { return &Files{client: c} }

// ForSession binds the file surface to one session.
func (f *Files) ForSession(sessionID string) *SessionFiles {
	return &SessionFiles{client: f.client, sessionID: sessionID}
}

// SessionFiles reaches the files one session owns.
//
// The routes behind this type publish no response schema, so [SessionFile] is a
// contract this package states rather than one the description declares. The
// conformance tests cannot cover it in either direction.
type SessionFiles struct {
	client    *Client
	sessionID string
}

// SessionID returns the session these files belong to.
func (s *SessionFiles) SessionID() string { return s.sessionID }

// SessionFile is one file a session owns.
//
// Hand-written: the file routes declare an empty response schema, so nothing in
// the description pins these fields. A server-side rename breaks this silently.
type SessionFile struct {
	// ID identifies the file, e.g. "file_abc123".
	ID string `json:"id"`

	// Filename is the name the file was uploaded under. nil when the server did
	// not report one.
	Filename *string `json:"filename,omitempty"`

	// Bytes is the file's size. nil when the server did not report it, which a
	// zero-length file does not.
	Bytes *int64 `json:"bytes,omitempty"`

	// CreatedAt is the Unix epoch second the file was created. nil when the server
	// did not report it.
	CreatedAt *int64 `json:"created_at,omitempty"`

	// MimeType is the content type the server recorded, when it recorded one.
	MimeType *string `json:"mime_type,omitempty"`

	// Raw is the response body this file decoded from, so a caller can reach a
	// field this package does not name. The routes publish no schema, so the set
	// above is what has been observed rather than what is guaranteed.
	//
	// [SessionFile.UnmarshalJSON] fills it. Empty only when the body was empty.
	Raw map[string]any `json:"-"`
}

// UnmarshalJSON decodes a file, keeping the body it decoded from.
//
// The named fields above are what this package has observed, not what the server
// guarantees: every file route publishes an empty response schema, so nothing
// pins them and nothing warns when the server adds a field. Keeping the body is
// what lets a caller reach one without waiting for this package to name it.
//
// The alias sheds this method, so the first decode does not recurse.
func (f *SessionFile) UnmarshalJSON(data []byte) error {
	type named SessionFile
	var decoded named
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*f = SessionFile(decoded)
	// Second pass rather than a json.RawMessage field, because a caller wants the
	// decoded map and not bytes they have to unmarshal again.
	if err := json.Unmarshal(data, &f.Raw); err != nil {
		return err
	}
	return nil
}

// ListFilesOptions tunes a file listing. The zero value asks for the server's
// default page.
type ListFilesOptions struct {
	// Limit caps one page. Zero leaves the server's default.
	Limit int

	// Order sets the direction. Empty leaves the server's default.
	Order SortOrder
}

func (o ListFilesOptions) query() url.Values {
	query := url.Values{}
	if o.Limit != 0 {
		query.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Order != "" {
		query.Set("order", string(o.Order))
	}
	return query
}

// List walks the session's files.
func (s *SessionFiles) List(ctx context.Context, opts ListFilesOptions) iter.Seq2[SessionFile, error] {
	if s.sessionID == "" {
		return errSeq[SessionFile](fmt.Errorf("list session files: %w: sessionID is required", ErrInvalidArgument))
	}
	return pageSeq(ctx, func(ctx context.Context, cursor string) (*Page[SessionFile], error) {
		query := opts.query()
		if cursor != "" {
			query.Set("after", cursor)
		}
		var page Page[SessionFile]
		if err := s.client.doJSON(ctx, http.MethodGet, s.segments(), query, nil, &page); err != nil {
			return nil, err
		}
		return &page, nil
	})
}

// Get returns one file's metadata.
func (s *SessionFiles) Get(ctx context.Context, fileID string) (*SessionFile, error) {
	if s.sessionID == "" || fileID == "" {
		return nil, fmt.Errorf("get session file: %w: sessionID and fileID are required", ErrInvalidArgument)
	}
	var file SessionFile
	if err := s.client.doJSON(ctx, http.MethodGet,
		append(s.segments(), fileID), nil, nil, &file); err != nil {
		return nil, err
	}
	return &file, nil
}

// Delete removes one file.
func (s *SessionFiles) Delete(ctx context.Context, fileID string) error {
	if s.sessionID == "" || fileID == "" {
		return fmt.Errorf("delete session file: %w: sessionID and fileID are required", ErrInvalidArgument)
	}
	return s.client.doJSON(ctx, http.MethodDelete, append(s.segments(), fileID), nil, nil, nil)
}

// segments is the route prefix for this session's files.
func (s *SessionFiles) segments() []string {
	return []string{"v1", "sessions", s.sessionID, "resources", "files"}
}

// Upload streams one file into the session.
//
// It takes a reader rather than a path, so a caller uploads from wherever the
// bytes are and this package holds none of them in memory: the multipart body is
// written to a pipe as the request drains it. Upstream takes a path, which is the
// one place its shape does not transfer.
func (s *SessionFiles) Upload(ctx context.Context, filename string, content io.Reader) (*SessionFile, error) {
	if s.sessionID == "" {
		return nil, fmt.Errorf("upload session file: %w: sessionID is required", ErrInvalidArgument)
	}
	if filename == "" {
		return nil, fmt.Errorf("upload session file: %w: filename is required", ErrInvalidArgument)
	}
	if bad, ok := headerUnsafeRune(filename); ok {
		return nil, fmt.Errorf("upload session file: %w: filename contains %q, which would forge a "+
			"multipart header", ErrInvalidArgument, bad)
	}
	if content == nil {
		return nil, fmt.Errorf("upload session file: %w: content is nil", ErrInvalidArgument)
	}

	body, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	// Close the read half on every return. doUpload can fail before it ever builds
	// a request — a rejected path segment, say — and then nothing else closes the
	// pipe, so the writer goroutine below blocks on its first write forever.
	defer func() { _ = body.Close() }()

	go func() {
		// Close the pipe writer on every path, so the request side always sees an
		// end — an error as an error, a short read as a short read. The reader half
		// is closed by the deferred Close above, which is what stops this goroutine
		// blocking when the request is never built.
		part, err := form.CreateFormFile("file", filename)
		if err == nil {
			_, err = io.Copy(part, content)
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
	}()

	var file SessionFile
	if err := s.client.doUpload(ctx, s.segments(), form.FormDataContentType(), body, &file); err != nil {
		return nil, err
	}
	return &file, nil
}

// Download writes one file's content to w, refusing to write more than maxBytes.
//
// The bound is required rather than optional. The server decides the length, and
// an unbounded copy from a remote server into a caller's memory or disk is a
// fault waiting for a large file. Pass a bound the caller can afford.
//
// w receives at most maxBytes. A larger body returns [ErrTruncated] with the
// count actually written, so a caller can tell a prefix from the whole file.
func (s *SessionFiles) Download(ctx context.Context, fileID string, w io.Writer, maxBytes int64) (int64, error) {
	if s.sessionID == "" || fileID == "" {
		return 0, fmt.Errorf("download session file: %w: sessionID and fileID are required", ErrInvalidArgument)
	}
	if maxBytes <= 0 {
		return 0, fmt.Errorf("download session file: %w: maxBytes must be positive", ErrInvalidArgument)
	}
	return s.client.doDownload(ctx, append(s.segments(), fileID, "content"), w, maxBytes)
}

// headerUnsafeRune reports the first rune in s that cannot appear in a MIME
// header value, and whether there was one.
//
// mime/multipart escapes a quote and a backslash in a filename and nothing else,
// so a CR or an LF passes through and ends the Content-Disposition line. What
// follows it is read as another header, or as another part. Callers derive a
// filename from a model or a user, so this is reachable input, and the check is
// a rejection rather than a rewrite: a name the caller did not choose is not the
// name they meant to upload under.
func headerUnsafeRune(s string) (rune, bool) {
	for _, r := range s {
		if r == '\r' || r == '\n' || r == 0 {
			return r, true
		}
	}
	return 0, false
}
