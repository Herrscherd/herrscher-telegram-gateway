package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

const (
	attachmentMaxBytes = 10 << 20
	stagingDirPerm     = 0o700
	stagedFilePerm     = 0o600
	stagingRootName    = "herrscher-attachments"
	photoName          = "photo.jpg"
	photoType          = "image/jpeg"
	fileNameMaxBytes   = 120
	extensionMaxBytes  = 16
)

var errUnreadableType = errors.New("file is not a type the agent can read")

type stager struct {
	root string
	log  *slog.Logger
}

func stagingRoot() string { return filepath.Join(os.TempDir(), stagingRootName) }

type incomingFile struct {
	fileID      string
	name        string
	contentType string
	size        int64
}

func (f incomingFile) readable() bool {
	return contracts.ModelReadable(contracts.Attachment{Filename: f.name, ContentType: f.contentType})
}

func filesOf(msg *tgctl.Message) []incomingFile {
	var files []incomingFile
	if msg.Document != nil {
		files = append(files, incomingFile{
			fileID: msg.Document.FileID, name: msg.Document.FileName,
			contentType: msg.Document.MimeType, size: msg.Document.FileSize,
		})
	}
	if photo, ok := largestPhoto(msg.Photo); ok {
		files = append(files, incomingFile{fileID: photo.FileID, name: photoName, contentType: photoType, size: photo.FileSize})
	}
	return files
}

func largestPhoto(sizes []tgctl.PhotoSize) (tgctl.PhotoSize, bool) {
	if len(sizes) == 0 {
		return tgctl.PhotoSize{}, false
	}
	best := sizes[0]
	for _, size := range sizes[1:] {
		if size.Width*size.Height > best.Width*best.Height {
			best = size
		}
	}
	return best, true
}

func (s stager) stage(ctx context.Context, api bot, conv address, msg *tgctl.Message) []contracts.Attachment {
	var staged []contracts.Attachment
	for index, file := range filesOf(msg) {
		attachment, err := s.stageOne(ctx, api, conv, msg.MessageID, index, file)
		if err != nil {
			s.log.Warn("attachment skipped", "file", file.name, "err", err)
			continue
		}
		staged = append(staged, attachment)
	}
	return staged
}

func (s stager) stageOne(ctx context.Context, api bot, conv address, messageID int64, index int, file incomingFile) (contracts.Attachment, error) {
	if !file.readable() {
		return contracts.Attachment{}, errUnreadableType
	}
	if file.size > attachmentMaxBytes {
		return contracts.Attachment{}, tgctl.ErrFileTooLarge
	}
	remote, err := api.GetFile(ctx, file.fileID)
	if err != nil {
		return contracts.Attachment{}, err
	}
	if remote.FileSize > attachmentMaxBytes {
		return contracts.Attachment{}, tgctl.ErrFileTooLarge
	}
	dir := filepath.Join(s.root, gatewayKind, conv.sessionName())
	if mkdirErr := os.MkdirAll(dir, stagingDirPerm); mkdirErr != nil {
		return contracts.Attachment{}, fmt.Errorf("create the staging directory: %w", mkdirErr)
	}
	name := strconv.FormatInt(messageID, 10) + "-" + strconv.Itoa(index) + "-" + safeFileName(file.name, remote.FilePath)
	target := filepath.Join(dir, name)
	size, err := s.write(ctx, api, target, remote.FilePath)
	if err != nil {
		return contracts.Attachment{}, err
	}
	location := url.URL{Scheme: "file", Path: target}
	return contracts.Attachment{Filename: name, URL: location.String(), ContentType: file.contentType, Size: int(size)}, nil
}

func (s stager) write(ctx context.Context, api bot, target, remotePath string) (int64, error) {
	out, err := os.OpenFile(filepath.Clean(target), os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, stagedFilePerm)
	if err != nil {
		return 0, fmt.Errorf("create the staged file: %w", err)
	}
	downloadErr := api.Download(ctx, remotePath, attachmentMaxBytes, out)
	err = errors.Join(downloadErr, out.Close())
	if err != nil {
		return 0, errors.Join(err, os.Remove(target))
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, fmt.Errorf("read the staged file: %w", err)
	}
	return info.Size(), nil
}

func safeFileName(name, remotePath string) string {
	if name == "" {
		name = filepath.Base(remotePath)
	}
	cleaned := strings.Map(func(char rune) rune {
		if char == '/' || char == '\\' || char < ' ' {
			return '_'
		}
		return char
	}, name)
	cleaned = strings.TrimLeft(cleaned, ".")
	if cleaned == "" {
		return "file"
	}
	ext := filepath.Ext(cleaned)
	if len(ext) > extensionMaxBytes {
		ext = ""
	}
	return clipBytes(strings.TrimSuffix(cleaned, filepath.Ext(cleaned)), fileNameMaxBytes-len(ext)) + ext
}

func clipBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := 0
	for index := range text {
		if index > limit {
			break
		}
		cut = index
	}
	return text[:cut]
}
