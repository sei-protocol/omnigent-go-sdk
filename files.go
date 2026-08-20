package omnigent

import (
	"context"
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

	// Filename is the name the file was uploaded under.
	Filename string `json:"filename,omitempty"`

	// Bytes is the file's size. Zero when the server does not report it.
	Bytes int64 `json:"bytes,omitempty"`

	// CreatedAt is the Unix epoch second the file was created.
	CreatedAt int64 `json:"created_at,omitempty"`

	// MimeType is the content type the server recorded, when it recorded one.
	MimeType string `json:"mime_type,omitempty"`

	// Raw is the response body this file decoded from, so a caller can reach a
	// field this package does not name. The routes publish no schema, so the set
	// above is what has been observed rather than what is guaranteed.
	Raw map[string]any `json:"-"`
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
	if content == nil {
		return nil, fmt.Errorf("upload session file: %w: content is nil", ErrInvalidArgument)
	}

	body, writer := io.Pipe()
	form := multipart.NewWriter(writer)

	go func() {
		// The pipe writer is the only thing this goroutine owns, and it closes it
		// on every path, so the request side always sees an end — an error as an
		// error, a short read as a short read, never a hang.
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
func (s *SessionFiles) Download(ctx context.Context, fileID string, w io.Writer, maxBytes int64) (int64, error) {
	if s.sessionID == "" || fileID == "" {
		return 0, fmt.Errorf("download session file: %w: sessionID and fileID are required", ErrInvalidArgument)
	}
	if maxBytes <= 0 {
		return 0, fmt.Errorf("download session file: %w: maxBytes must be positive", ErrInvalidArgument)
	}
	return s.client.doDownload(ctx, append(s.segments(), fileID, "content"), w, maxBytes)
}
