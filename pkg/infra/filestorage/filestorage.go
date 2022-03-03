package filestorage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/services/featuremgmt"
	"github.com/grafana/grafana/pkg/setting"
	"gocloud.dev/blob"

	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/memblob"
)

const (
	ServiceName = "FileStorage"
)

func ProvideService(features featuremgmt.FeatureToggles, cfg *setting.Cfg) (FileStorage, error) {
	logger := log.New("fileStorageLogger")

	backendByName := make(map[string]FileStorage)
	dummyBackend := &wrapper{
		log:                 logger,
		wrapped:             &dummyFileStorage{},
		pathFilters:         &PathFilters{allowedPrefixes: []string{}},
		supportedOperations: []Operation{},
	}

	if !features.IsEnabled(featuremgmt.FlagFileStoreApi) {
		logger.Info("Filestorage API disabled")
		return &service{
			backendByName: backendByName,
			dummyBackend:  dummyBackend,
			log:           logger,
		}, nil
	}

	fsConfig := newConfig(cfg.StaticRootPath)

	// TODO IMPLEMENT
	//for _, dbBackend := range fsConfig.Backends.DB {
	//
	//}

	for _, fsBackend := range fsConfig.Backends.FS {
		fsBackendLogger := log.New("fileStorage-" + fsBackend.Name)
		path := fmt.Sprintf("file://%s", fsBackend.Path)
		bucket, err := blob.OpenBucket(context.Background(), path)
		if err != nil {
			currentDir, _ := os.Getwd()
			logger.Error("Failed to initialize grafana ds storage", "path", path, "error", err, "cwd", currentDir)
			return nil, err
		}

		if _, ok := backendByName[fsBackend.Name]; ok {
			return nil, errors.New("Duplicate backend name " + fsBackend.Name)
		}

		pathFilters := &PathFilters{allowedPrefixes: fsBackend.AllowedPrefixes}
		backendByName[fsBackend.Name] = NewCdkBlobStorage(fsBackendLogger, bucket, "", pathFilters, fsBackend.SupportedOperations)
	}

	return &service{
		backendByName: backendByName,
		dummyBackend:  dummyBackend,
		log:           logger,
	}, nil
}

type service struct {
	log           log.Logger
	dummyBackend  FileStorage
	backendByName map[string]FileStorage
}

func removeBackendNamePrefix(path string) string {
	path = strings.TrimPrefix(path, Delimiter)
	if path == Delimiter || path == "" {
		return Delimiter
	}

	if !strings.Contains(path, Delimiter) {
		return Delimiter
	}

	split := strings.Split(path, Delimiter)

	// root of storage
	if len(split) == 2 && split[1] == "" {
		return Delimiter
	}

	// replace storage
	split[0] = ""
	return strings.Join(split, Delimiter)
}

func (b service) getBackend(path string) (FileStorage, string, string, error) {
	for backendName, backend := range b.backendByName {
		if strings.HasPrefix(path, Delimiter+backendName) || backendName == path {
			backendSpecificPath := removeBackendNamePrefix(path)
			if err := validatePath(backendSpecificPath); err != nil {
				return nil, "", "", err
			}
			return backend, backendSpecificPath, backendName, nil
		}
	}

	if err := validatePath(path); err != nil {
		return nil, "", "", err
	}
	b.log.Warn("Backend not found", "path", path)
	return b.dummyBackend, path, "", nil
}

func (b service) Get(ctx context.Context, path string) (*File, error) {
	backend, backendSpecificPath, backendName, err := b.getBackend(path)
	if err != nil {
		return nil, err
	}

	file, err := backend.Get(ctx, backendSpecificPath)
	if file != nil {
		file.FullPath = Join(backendName, file.FullPath)
	}
	return file, err
}

func (b service) Delete(ctx context.Context, path string) error {
	backend, backendSpecificPath, _, err := b.getBackend(path)
	if err != nil {
		return err
	}

	return backend.Delete(ctx, backendSpecificPath)
}

func (b service) Upsert(ctx context.Context, file *UpsertFileCommand) error {
	backend, backendSpecificPath, _, err := b.getBackend(file.Path)
	if err != nil {
		return err
	}

	file.Path = backendSpecificPath
	return backend.Upsert(ctx, file)
}

func (b service) ListFiles(ctx context.Context, path string, cursor *Paging, options *ListOptions) (*ListFilesResponse, error) {
	backend, backendSpecificPath, backendName, err := b.getBackend(path)
	if err != nil {
		return nil, err
	}

	resp, err := backend.ListFiles(ctx, backendSpecificPath, cursor, options)
	if resp != nil && resp.Files != nil {
		for i := range resp.Files {
			resp.Files[i].FullPath = Join(backendName, resp.Files[i].FullPath)
		}
	}
	return resp, err
}

func (b service) ListFolders(ctx context.Context, path string, options *ListOptions) ([]FileMetadata, error) {
	backend, backendSpecificPath, backendName, err := b.getBackend(path)
	if err != nil {
		return nil, err
	}

	folders, err := backend.ListFolders(ctx, backendSpecificPath, options)
	if folders != nil {
		for i := range folders {
			folders[i].FullPath = Join(backendName, folders[i].FullPath)
		}
	}

	return folders, err
}

func (b service) CreateFolder(ctx context.Context, path string) error {
	backend, backendSpecificPath, _, err := b.getBackend(path)
	if err != nil {
		return err
	}

	return backend.CreateFolder(ctx, backendSpecificPath)
}

func (b service) DeleteFolder(ctx context.Context, path string) error {
	backend, backendSpecificPath, _, err := b.getBackend(path)
	if err != nil {
		return err
	}

	return backend.DeleteFolder(ctx, backendSpecificPath)
}

func (b service) close() error {
	var lastError error
	for _, backend := range b.backendByName {
		lastError = backend.close()
	}

	return lastError
}
