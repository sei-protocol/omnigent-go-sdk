package omnigent

// Ptr returns a pointer to v.
//
// Every optional field in this package is a pointer, so that a caller can tell
// "the server sent zero" from "the server sent nothing", and so that a request
// can distinguish "set this to the empty string" from "leave it alone". Ptr is
// how a caller writes the former without declaring a variable per field.
//
//	client.SetModelOverride(ctx, id, omnigent.Ptr(""))   // clear the override
//	client.SetModelOverride(ctx, id, nil)                // leave it alone
//
// It lives in its own file because it belongs to no one type.
func Ptr[T any](v T) *T { return &v }
