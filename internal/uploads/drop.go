package uploads

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	tushandler "github.com/tus/tusd/v2/pkg/handler"

	"filebrowser/internal/files"
	"filebrowser/internal/shares"
)

// What the tus path and the bucket path share about who an upload belongs
// to and what a drop link lets through.

// An upload's owner is who may continue or cancel it: a user, or the
// holders of a drop link.
const (
	userOwnerPrefix  = "u:"
	shareOwnerPrefix = "s:"
)

func userOwner(id int64) string  { return userOwnerPrefix + strconv.FormatInt(id, 10) }
func shareOwner(id int64) string { return shareOwnerPrefix + strconv.FormatInt(id, 10) }

// ownerShare is the id of the drop link that owner names, if it names one.
func ownerShare(owner string) (int64, bool) {
	v, ok := strings.CutPrefix(owner, shareOwnerPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(v, 10, 64)
	return id, err == nil
}

const (
	// metaUploader is the upload metadata key of the name an uploader gave.
	metaUploader = "uploader"
	// maxUploaderName bounds that name, in characters.
	maxUploaderName = 64
	// unknownType is what a file name says when it says nothing.
	unknownType = "application/octet-stream"
)

// dropPlan decides where an upload of size bytes through a drop link goes,
// and whether the link takes it at all: the uploader's name where it is
// asked for, the size per file, and the kind of file by its name (so that
// one of the wrong kind is refused before it is sent; what was received is
// looked at when it is placed). Errors are rejections to show the uploader.
func (u *Uploads) dropPlan(res *shares.Resolved, meta tushandler.MetaData, size int64) (dir, name string, lim shares.Limits, err error) {
	base := res.Root
	if res.Share.AskName {
		// Each uploader's files go into a folder of the name they gave.
		who := strings.TrimSpace(meta[metaUploader])
		if files.ValidName(who) != nil || utf8.RuneCountInString(who) > maxUploaderName {
			return "", "", lim, reject(http.StatusBadRequest, "enter your name first")
		}
		base = files.Join(base, who)
	}
	if dir, name, err = target(base, meta); err != nil {
		return "", "", lim, err
	}
	dir, err = u.files.FS.Authorize(res.Access, dir, files.PermWrite)
	if err != nil || !files.Within(dir, res.Root) {
		return "", "", lim, reject(http.StatusForbidden, "forbidden")
	}
	lim = u.shares.Limits(res.Share)
	if lim.FileBytes > 0 && size > lim.FileBytes {
		return "", "", lim, reject(http.StatusRequestEntityTooLarge, "this file is larger than the link accepts")
	}
	if t := files.MimeType(name); t != unknownType && !lim.Allows(t) {
		return "", "", lim, reject(http.StatusUnsupportedMediaType, "this link does not accept this kind of file")
	}
	return dir, name, lim, nil
}

// reserveDrop counts an upload of size bytes against a drop link's total,
// or rejects it when the link is full.
func (u *Uploads) reserveDrop(ctx context.Context, shareID, size int64, lim shares.Limits) error {
	ok, err := u.shares.Store.ReserveShareUpload(ctx, shareID, size, lim.Bytes, lim.Files)
	if err != nil {
		slog.Error("reserve drop upload", "share", shareID, "err", err)
		return reject(http.StatusInternalServerError, "internal error")
	}
	if !ok {
		return reject(http.StatusRequestEntityTooLarge, "this link has reached its upload limit")
	}
	return nil
}

// releaseDrop gives a drop link back what an upload that will not be
// finished had counted against it; for any other owner it does nothing.
func (u *Uploads) releaseDrop(ctx context.Context, owner string, size int64) {
	if id, ok := ownerShare(owner); ok {
		if err := u.shares.Store.ReleaseShareUpload(context.WithoutCancel(ctx), id, size); err != nil {
			slog.Error("release drop upload", "share", id, "err", err)
		}
	}
}

// rejection splits an error made by reject into the status and the text to
// answer with; any other error is an internal one.
func rejection(err error) (status int, msg string) {
	var te tushandler.Error
	if errors.As(err, &te) && te.HTTPResponse.StatusCode != 0 {
		return te.HTTPResponse.StatusCode, te.Message
	}
	return http.StatusInternalServerError, "internal error"
}
