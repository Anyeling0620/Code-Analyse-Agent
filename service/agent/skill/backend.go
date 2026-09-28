package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"edu.agent.code/utils/logger"
	"github.com/cloudwego/eino/adk/filesystem"
	einoskill "github.com/cloudwego/eino/adk/middlewares/skill"
)

type DirectoryBackend struct {
	directories []string
	backends    []einoskill.Backend
}

func NewDirectoryBackend(ctx context.Context, directories []string) (*DirectoryBackend, error) {
	skillDirectories := make([]string, 0, len(directories))
	backends := make([]einoskill.Backend, 0, len(directories))
	uniq := make(map[string]bool, len(directories))
	for _, directory := range directories {
		path, err := resolvedDirectory(directory)
		if err != nil {
			return nil, err
		}
		if path == "" || uniq[path] {
			continue
		}
		uniq[path] = true
		backend, err := einoskill.NewBackendFromFilesystem(ctx, &einoskill.BackendFromFilesystemConfig{
			Backend: &osBackend{},
			BaseDir: path,
		})
		if err != nil {
			return nil, err
		}
		skillDirectories = append(skillDirectories, path)
		backends = append(backends, backend)
	}
	return &DirectoryBackend{
		directories: skillDirectories,
		backends:    backends,
	}, nil
}

func (b *DirectoryBackend) List(ctx context.Context) ([]einoskill.FrontMatter, error) {
	if b == nil {
		return nil, nil
	}
	var output []einoskill.FrontMatter
	uniq := make(map[string]string)
	for idx, backend := range b.backends {
		items, err := backend.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.Name == "" {
				return nil, fmt.Errorf("skill in %s has empty name", b.directories[idx])
			}
			_, ok := uniq[item.Name]
			if ok {
				logger.Warn("skill name %s in %s is duplicated", item.Name, b.directories[idx])
				continue
			}
			uniq[item.Name] = b.directories[idx]
			output = append(output, item)
		}
	}
	sort.SliceStable(output, func(i, j int) bool {
		return output[i].Name < output[j].Name
	})
	return output, nil
}
func (b *DirectoryBackend) Get(ctx context.Context, name string) (einoskill.Skill, error) {
	if b == nil {
		return einoskill.Skill{}, fmt.Errorf("cannot get skill %s", name)
	}
	name = strings.TrimSpace(name)
	for _, backend := range b.backends {
		item, err := backend.Get(ctx, name)
		if err == nil {
			return item, nil
		}
	}
	return einoskill.Skill{}, fmt.Errorf("skill not found %s", name)
}

type osBackend struct {
}

func (b *osBackend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	base := strings.TrimSpace(req.Path)
	if base == "" {
		base = "."
	}
	matches, err := filepath.Glob(filepath.Join(base, filepath.FromSlash(req.Pattern)))
	if err != nil {
		return nil, err
	}
	output := make([]filesystem.FileInfo, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		output = append(output, filesystem.FileInfo{
			Path:       match,
			IsDir:      info.IsDir(),
			ModifiedAt: info.ModTime().Format(time.DateTime),
			Size:       info.Size(),
		})
	}
	return output, nil
}
func (b *osBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	data, err := os.ReadFile(req.FilePath)
	if err != nil {
		return nil, err
	}
	content := string(data)
	offset := req.Offset - 1
	if offset < 0 {
		offset = 0
	}
	if offset == 0 && req.Limit <= 0 {
		return &filesystem.FileContent{
			Content: content,
		}, nil
	}
	lines := strings.Split(content, "\n")
	if offset >= len(lines) {
		return &filesystem.FileContent{
			Content: "",
		}, nil
	}
	end := len(lines)
	if req.Limit > 0 && offset+req.Limit < end {
		end = offset + req.Limit
	}

	return &filesystem.FileContent{
		Content: strings.Join(lines[offset:end], "\n"),
	}, nil
}
func (b *osBackend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	return nil, errors.New("not implemented")
}
func (b *osBackend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	return nil, errors.New("not implemented")
}
func (b *osBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	return errors.New("not implemented")
}
func (b *osBackend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	return errors.New("not implemented")
}

func resolvedDirectory(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", nil
	}
	dir = expandHome(dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
func expandHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
