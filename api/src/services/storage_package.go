/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The storage package repository (shared-storage-design.md §6.1): installers of storage software uploaded in parts
// to S3, verified in the background, and usable for deployments once a system admin accepts their license. Only the
// kinds whose backend asks for a package use it (GPFS); hosts download the installer with a presigned URL.

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// Parts are sent through nginx (10 MB per request) and cpgateway (reads a whole request into memory); S3 wants
	// at least 5 MiB for every part but the last and at most 10000 parts
	StoragePackagePartSize   = 8 << 20
	storagePackageMaxParts   = 10000
	storagePackagePrefix     = "storage-packages"
	storagePackageStaleAfter = 24 * time.Hour
	// A verification reads the whole installer once (about two minutes for 1.7 GB); one still running after this
	// long belonged to a clapi that went away and is started again
	storagePackageVerifyStale = 30 * time.Minute
	// Hosts download the installer with a presigned URL that holds this long
	storagePackageURLValidity = 6 * time.Hour
	// The installer header (a shell script) is read line by line up to its payload
	storagePackageHeaderMaxLines = 2000
	storagePackageMetaMax        = 4 << 20
)

var StoragePackages = &StoragePackageAdmin{}

type StoragePackageAdmin struct{}

var (
	storagePackageNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,199}$`)
	gpfsDistroRe         = regexp.MustCompile(`^gpfs_debs/ubuntu/(ubuntu[0-9]+)$`)
	gpfsLicenseDebRe     = regexp.MustCompile(`^gpfs_debs/gpfs\.license\.([a-z]+)_`)
	gpfsBaseDebRe        = regexp.MustCompile(`^gpfs_debs/gpfs\.base_([0-9][0-9.]*-[0-9]+)_`)
	// Languages of the license text kept for the UI; the installer has many more
	storagePackageLanguages = []string{"en", "zh", "zh_TW"}
	// Edition of an installer, from the name of its license package
	gpfsEditions = map[string]string{"ec": "erasure_code", "dm": "data_management", "da": "data_access", "std": "standard", "dev": "developer"}
)

func storageS3Core() (*minio.Core, error) {
	client := s3Client.Load()
	if client == nil {
		return nil, NewCLError(ErrStorageNeedsS3, "Storage packages are kept in S3, which is not configured or not reachable", nil)
	}
	return &minio.Core{Client: client}, nil
}

// storagePackageParts is how many parts an upload of size bytes has
func storagePackageParts(size int64) int32 {
	return int32((size + StoragePackagePartSize - 1) / StoragePackagePartSize)
}

func (a *StoragePackageAdmin) needsPackage(kind string) error {
	backend, err := storageBackendOf(kind)
	if err != nil {
		return err
	}
	if !backend.Requirements().Package {
		return planError("A %s cluster installs from the distribution and needs no package", kind)
	}
	return nil
}

// StartUpload records a package and starts its multipart upload to S3
func (a *StoragePackageAdmin) StartUpload(ctx context.Context, kind, fileName string, size int64) (pkg *model.StoragePackage, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if err = a.needsPackage(kind); err != nil {
		return
	}
	if !storagePackageNameRe.MatchString(fileName) {
		return nil, NewCLError(ErrInvalidParameter, "The file name may only hold letters, digits and . _ + -", nil)
	}
	if size <= 0 || size > int64(StoragePackagePartSize)*storagePackageMaxParts {
		return nil, NewCLError(ErrInvalidParameter, "Invalid package size", nil)
	}
	core, err := storageS3Core()
	if err != nil {
		return nil, err
	}
	id := uuid.New().String()
	key := fmt.Sprintf("%s/%s/%s", storagePackagePrefix, id, fileName)
	uploadID, err := core.NewMultipartUpload(ctx, s3Bucket, key, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return nil, NewCLError(ErrStorageNeedsS3, "Failed to start the upload to S3", err)
	}
	now := time.Now()
	pkg = &model.StoragePackage{Model: model.Model{UUID: id}, Kind: kind, FileName: fileName, SizeBytes: size, ObjectKey: key,
		UploadID: uploadID, Status: model.StoragePackageUploading, ProgressAt: &now}
	if err = dbs.DBContext(ctx).Create(pkg).Error; err != nil {
		_ = core.AbortMultipartUpload(context.WithoutCancel(ctx), s3Bucket, key, uploadID)
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to record the package", err)
	}
	return
}

func (a *StoragePackageAdmin) get(ctx context.Context, id string) (*model.StoragePackage, error) {
	pkg := &model.StoragePackage{}
	if err := dbs.DBContext(ctx).Where("uuid = ?", id).Take(pkg).Error; err != nil {
		return nil, NewCLError(ErrStoragePackageNotFound, "Storage package not found", err)
	}
	return pkg, nil
}

// UploadPart takes part n (from 1) of an upload. Parts come in order: part n is taken once part n-1 is, and part n
// may be sent again when the answer to it was lost. Every part is StoragePackagePartSize bytes but the last
func (a *StoragePackageAdmin) UploadPart(ctx context.Context, id string, n int32, body io.Reader, size int64) (pkg *model.StoragePackage, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if pkg, err = a.get(ctx, id); err != nil {
		return
	}
	if pkg.Status != model.StoragePackageUploading || pkg.UploadID == "" {
		return nil, NewCLError(ErrStoragePackageState, "The package is not being uploaded", nil)
	}
	total := storagePackageParts(pkg.SizeBytes)
	if n < 1 || n > total || (n != pkg.PartsDone+1 && n != pkg.PartsDone) {
		return nil, NewCLError(ErrStoragePackageState, fmt.Sprintf("Part %d is out of order: %d of %d parts are done", n, pkg.PartsDone, total), nil)
	}
	want := int64(StoragePackagePartSize)
	if n == total {
		want = pkg.SizeBytes - int64(total-1)*StoragePackagePartSize
	}
	if size != want {
		return nil, NewCLError(ErrInvalidParameter, fmt.Sprintf("Part %d must be %d bytes, got %d", n, want, size), nil)
	}
	core, err := storageS3Core()
	if err != nil {
		return nil, err
	}
	if _, err = core.PutObjectPart(ctx, s3Bucket, pkg.ObjectKey, pkg.UploadID, int(n), io.LimitReader(body, size), size, minio.PutObjectPartOptions{}); err != nil {
		return nil, NewCLError(ErrStorageNeedsS3, fmt.Sprintf("Failed to store part %d in S3", n), err)
	}
	now := time.Now()
	db := dbs.DBContext(ctx)
	if err = db.Model(&model.StoragePackage{}).Where("id = ? AND parts_done < ?", pkg.ID, n).
		Updates(map[string]interface{}{"parts_done": n, "progress_at": &now}).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to record the part", err)
	}
	return a.get(ctx, id)
}

// CompleteUpload joins the parts once all are in, and starts the verification in the background
func (a *StoragePackageAdmin) CompleteUpload(ctx context.Context, id string) (pkg *model.StoragePackage, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if pkg, err = a.get(ctx, id); err != nil {
		return
	}
	if pkg.Status != model.StoragePackageUploading || pkg.UploadID == "" {
		return nil, NewCLError(ErrStoragePackageState, "The package is not being uploaded", nil)
	}
	if total := storagePackageParts(pkg.SizeBytes); pkg.PartsDone != total {
		return nil, NewCLError(ErrStoragePackageState, fmt.Sprintf("%d of %d parts are uploaded", pkg.PartsDone, total), nil)
	}
	core, err := storageS3Core()
	if err != nil {
		return nil, err
	}
	parts := []minio.CompletePart{}
	marker := 0
	for {
		res, lerr := core.ListObjectParts(ctx, s3Bucket, pkg.ObjectKey, pkg.UploadID, marker, 1000)
		if lerr != nil {
			return nil, NewCLError(ErrStorageNeedsS3, "Failed to list the uploaded parts", lerr)
		}
		for _, p := range res.ObjectParts {
			parts = append(parts, minio.CompletePart{PartNumber: p.PartNumber, ETag: p.ETag})
		}
		if !res.IsTruncated {
			break
		}
		marker = res.NextPartNumberMarker
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	if _, err = core.CompleteMultipartUpload(ctx, s3Bucket, pkg.ObjectKey, pkg.UploadID, parts, minio.PutObjectOptions{}); err != nil {
		return nil, NewCLError(ErrStorageNeedsS3, "Failed to complete the upload in S3", err)
	}
	now := time.Now()
	if err = dbs.DBContext(ctx).Model(pkg).Updates(map[string]interface{}{"upload_id": "", "status": model.StoragePackageVerifying,
		"progress_at": &now}).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to record the package", err)
	}
	go verifyStoragePackage(context.WithoutCancel(ctx), pkg.ID)
	return a.get(ctx, id)
}

// StartDownload records a package that clapi fetches from a URL itself, then verifies
func (a *StoragePackageAdmin) StartDownload(ctx context.Context, kind, rawURL string) (pkg *model.StoragePackage, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if err = a.needsPackage(kind); err != nil {
		return
	}
	u, perr := url.Parse(rawURL)
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(rawURL, " \t\r\n") {
		return nil, NewCLError(ErrInvalidParameter, "The download URL must be an http or https address", perr)
	}
	name := u.Path[strings.LastIndex(u.Path, "/")+1:]
	if !storagePackageNameRe.MatchString(name) {
		name = "installer"
	}
	if _, err = storageS3Core(); err != nil {
		return nil, err
	}
	id := uuid.New().String()
	now := time.Now()
	pkg = &model.StoragePackage{Model: model.Model{UUID: id}, Kind: kind, FileName: name, SourceURL: rawURL,
		ObjectKey: fmt.Sprintf("%s/%s/%s", storagePackagePrefix, id, name), Status: model.StoragePackageUploading, ProgressAt: &now}
	if err = dbs.DBContext(ctx).Create(pkg).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to record the package", err)
	}
	go downloadStoragePackage(context.WithoutCancel(ctx), pkg.ID)
	return
}

func downloadStoragePackage(ctx context.Context, id int64) {
	db := dbs.DBContext(ctx)
	pkg := &model.StoragePackage{}
	if err := db.Take(pkg, id).Error; err != nil {
		return
	}
	fail := func(reason string) {
		db.Model(pkg).Updates(map[string]interface{}{"status": model.StoragePackageError, "reason": truncate(reason, 512)})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pkg.SourceURL, nil)
	if err != nil {
		fail("bad download URL: " + err.Error())
		return
	}
	resp, err := (&http.Client{Timeout: S3UploadTimeout()}).Do(req)
	if err != nil {
		fail("download failed: " + err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fail(fmt.Sprintf("download failed: HTTP %d", resp.StatusCode))
		return
	}
	counted := &countingReader{r: resp.Body}
	if err = S3PutObject(ctx, pkg.ObjectKey, counted); err != nil {
		fail("storing the download in S3 failed: " + err.Error())
		return
	}
	now := time.Now()
	db.Model(pkg).Updates(map[string]interface{}{"size_bytes": counted.n, "status": model.StoragePackageVerifying, "progress_at": &now})
	verifyStoragePackage(ctx, id)
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// storagePackageMeta is what the verification finds in an installer
type storagePackageMeta struct {
	SHA256      string
	Size        int64
	PayloadLine int32
	Version     string
	Edition     string
	Distros     []string
	Manifest    map[string]string // deb file name -> md5
	License     map[string]string // language -> text
}

// verifyStoragePackage reads an installer from S3 and records what it holds. It only reads, nothing in the
// installer is run (and no Java is needed for its license tool)
func verifyStoragePackage(ctx context.Context, id int64) {
	db := dbs.DBContext(ctx)
	pkg := &model.StoragePackage{}
	if err := db.Take(pkg, id).Error; err != nil {
		return
	}
	client := s3Client.Load()
	if client == nil {
		db.Model(pkg).Updates(map[string]interface{}{"status": model.StoragePackageError, "reason": "S3 is not reachable"})
		return
	}
	obj, err := client.GetObject(ctx, s3Bucket, pkg.ObjectKey, minio.GetObjectOptions{})
	if err == nil {
		defer obj.Close()
	}
	var meta *storagePackageMeta
	if err == nil {
		meta, err = parseGPFSInstaller(obj)
	}
	if err != nil {
		logger.Ctx(ctx).Errorf("Storage package %d failed verification: %v", id, err)
		db.Model(pkg).Updates(map[string]interface{}{"status": model.StoragePackageError, "reason": truncate("verification failed: "+err.Error(), 512)})
		return
	}
	distros, _ := json.Marshal(meta.Distros)
	manifest, _ := json.Marshal(meta.Manifest)
	license, _ := json.Marshal(meta.License)
	db.Model(pkg).Updates(map[string]interface{}{"status": model.StoragePackageReady, "reason": "", "sha256": meta.SHA256,
		"size_bytes": meta.Size, "payload_line": meta.PayloadLine, "version": meta.Version, "edition": meta.Edition,
		"distros": string(distros), "manifest": string(manifest), "license_text": string(license)})
}

// parseGPFSInstaller reads an IBM Storage Scale installer: a shell script whose line PGM_BEGIN_TGZ on is a tar.gz
// with the packages (gpfs_debs/...), their md5 sums (manifest) and the license texts (LA_HOME, UTF-16). The whole
// file is read so its SHA-256 covers every byte
func parseGPFSInstaller(r io.Reader) (*storagePackageMeta, error) {
	hash := sha256.New()
	counted := &countingReader{r: io.TeeReader(r, hash)}
	br := bufio.NewReaderSize(counted, 1<<20)
	meta := &storagePackageMeta{Manifest: map[string]string{}, License: map[string]string{}}
	prodFiles := ""
	for line := int32(1); ; line++ {
		if meta.PayloadLine > 0 && line == meta.PayloadLine {
			break
		}
		if line > storagePackageHeaderMaxLines {
			return nil, fmt.Errorf("no PGM_BEGIN_TGZ in the first %d lines: not an IBM Storage Scale installer", storagePackageHeaderMaxLines)
		}
		text, err := br.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("the installer ends inside its header: %v", err)
		}
		text = strings.TrimRight(text, "\r\n")
		if v, ok := strings.CutPrefix(text, "PGM_BEGIN_TGZ="); ok && meta.PayloadLine == 0 {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n <= int(line) || n > storagePackageHeaderMaxLines {
				return nil, fmt.Errorf("invalid PGM_BEGIN_TGZ %q", v)
			}
			meta.PayloadLine = int32(n)
		}
		if v, ok := strings.CutPrefix(text, "PROD_FILES="); ok {
			prodFiles = strings.Trim(v, `"'`)
		}
	}
	for _, f := range strings.Fields(prodFiles) {
		if m := gpfsDistroRe.FindStringSubmatch(f); m != nil {
			meta.Distros = append(meta.Distros, m[1])
		}
	}
	gz, err := gzip.NewReader(br)
	if err != nil {
		return nil, fmt.Errorf("the payload is not gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the payload: %v", err)
		}
		name := strings.TrimPrefix(h.Name, "./")
		switch {
		case name == "manifest":
			body, err := io.ReadAll(io.LimitReader(tr, storagePackageMetaMax))
			if err != nil {
				return nil, err
			}
			for _, l := range strings.Split(string(body), "\n") {
				// sw,deb,<file>,md5sum:<md5>   (rpm lines carry a date before the sum)
				fields := strings.Split(strings.TrimSpace(l), ",")
				if len(fields) < 4 || fields[1] != "deb" {
					continue
				}
				if md5, ok := strings.CutPrefix(strings.TrimSpace(fields[len(fields)-1]), "md5sum:"); ok {
					meta.Manifest[strings.TrimSpace(fields[2])] = md5
				}
			}
		case strings.HasPrefix(name, "LA_HOME/LA_") || strings.HasPrefix(name, "LA_HOME/LI_"):
			lang := name[len("LA_HOME/LA_"):]
			wanted := false
			for _, l := range storagePackageLanguages {
				wanted = wanted || l == lang
			}
			if !wanted {
				continue
			}
			body, err := io.ReadAll(io.LimitReader(tr, storagePackageMetaMax))
			if err != nil {
				return nil, err
			}
			text := decodeUTF16(body)
			// The agreement first, then the license information of the programs
			if strings.HasPrefix(name, "LA_HOME/LA_") {
				meta.License[lang] = text + meta.License[lang]
			} else {
				meta.License[lang] = meta.License[lang] + "\n\n" + text
			}
		default:
			if m := gpfsLicenseDebRe.FindStringSubmatch(name); m != nil {
				meta.Edition = m[1]
				if e, ok := gpfsEditions[m[1]]; ok {
					meta.Edition = e
				}
			}
			if m := gpfsBaseDebRe.FindStringSubmatch(name); m != nil {
				meta.Version = strings.ReplaceAll(m[1], "-", ".")
			}
		}
	}
	// Anything after the archive still counts for the checksum
	if _, err = io.Copy(io.Discard, br); err != nil {
		return nil, err
	}
	meta.SHA256 = hex.EncodeToString(hash.Sum(nil))
	meta.Size = counted.n
	switch {
	case meta.Version == "":
		return nil, fmt.Errorf("no gpfs.base package in the installer")
	case meta.Manifest["gpfs.base_"+gpfsDebVersion(meta.Version)+"_amd64.deb"] == "":
		return nil, fmt.Errorf("the manifest has no md5 sum for gpfs.base")
	case len(meta.Distros) == 0:
		return nil, fmt.Errorf("the installer has packages for no Ubuntu release")
	case meta.License["en"] == "":
		return nil, fmt.Errorf("the installer has no license text")
	}
	sort.Strings(meta.Distros)
	return meta, nil
}

// gpfsDebVersion turns 6.0.0.2 back into the 6.0.0-2 of the package file names
func gpfsDebVersion(version string) string {
	if i := strings.LastIndex(version, "."); i > 0 {
		return version[:i] + "-" + version[i+1:]
	}
	return version
}

// decodeUTF16 decodes the UTF-16 license files of the installer (little endian, with a byte order mark)
func decodeUTF16(b []byte) string {
	bigEndian := false
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		bigEndian, b = true, b[2:]
	} else if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	return strings.ReplaceAll(string(utf16.Decode(u)), "\r\n", "\n")
}

// AcceptLicense records that a system admin accepted the license of a verified package
func (a *StoragePackageAdmin) AcceptLicense(ctx context.Context, id string) (pkg *model.StoragePackage, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if pkg, err = a.get(ctx, id); err != nil {
		return
	}
	if pkg.Status != model.StoragePackageReady {
		return nil, NewCLError(ErrStoragePackageState, "The package is not verified yet", nil)
	}
	now := time.Now()
	if err = dbs.DBContext(ctx).Model(pkg).Updates(map[string]interface{}{"accepted_by": GetMemberShip(ctx).UserName, "accepted_at": &now}).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to record the acceptance", err)
	}
	return a.get(ctx, id)
}

// Delete removes a package that no cluster uses, with its object in S3 (or the parts of an unfinished upload)
func (a *StoragePackageAdmin) Delete(ctx context.Context, id string) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	pkg, err := a.get(ctx, id)
	if err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(pkg, pkg.ID).Error; err != nil {
			return NewCLError(ErrStoragePackageNotFound, "Storage package not found", err)
		}
		var users int64
		tx.Model(&model.StorageCluster{}).Where("package_id = ?", pkg.ID).Count(&users)
		if users > 0 {
			return NewCLError(ErrStoragePackageState, fmt.Sprintf("%d storage cluster(s) use this package", users), nil)
		}
		if err := tx.Delete(pkg).Error; err != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to delete the package", err)
		}
		removeStoragePackageObject(context.WithoutCancel(ctx), pkg)
		return nil
	})
}

func removeStoragePackageObject(ctx context.Context, pkg *model.StoragePackage) {
	core, err := storageS3Core()
	if err != nil {
		return
	}
	if pkg.UploadID != "" {
		if err = core.AbortMultipartUpload(ctx, s3Bucket, pkg.ObjectKey, pkg.UploadID); err != nil {
			logger.Ctx(ctx).Warningf("Failed to abort the upload of storage package %d: %v", pkg.ID, err)
		}
	}
	if err = S3RemoveObject(ctx, pkg.ObjectKey); err != nil {
		logger.Ctx(ctx).Warningf("Failed to remove storage package %d from S3: %v", pkg.ID, err)
	}
}

func (a *StoragePackageAdmin) List(ctx context.Context, offset, limit int64) (total int64, pkgs []*model.StoragePackage, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	if err = db.Model(&model.StoragePackage{}).Count(&total).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to count storage packages", err)
	}
	pkgs = []*model.StoragePackage{}
	// The list leaves out the license texts and manifests, which are tens of KiB each
	err = db.Omit("license_text", "manifest").Order("id DESC").Offset(int(offset)).Limit(int(limit)).Find(&pkgs).Error
	return
}

func (a *StoragePackageAdmin) Get(ctx context.Context, id string) (*model.StoragePackage, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	return a.get(ctx, id)
}

// storagePackageURL is the presigned URL hosts download an installer with
func storagePackageURL(ctx context.Context, pkg *model.StoragePackage) (string, error) {
	client := s3Client.Load()
	if client == nil {
		return "", fmt.Errorf("S3 is not reachable")
	}
	u, err := client.PresignedGetObject(ctx, s3Bucket, pkg.ObjectKey, storagePackageURLValidity, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// maintainStoragePackages aborts uploads that stopped for a day (MinIO keeps unfinished parts forever) and starts
// again the verifications a clapi that went away left half done. Runs in the storage leader loop
func maintainStoragePackages(ctx context.Context) {
	db := dbs.DBContext(ctx)
	stale := []*model.StoragePackage{}
	db.Where("status = ? AND progress_at < ?", model.StoragePackageUploading, time.Now().Add(-storagePackageStaleAfter)).Find(&stale)
	for _, pkg := range stale {
		removeStoragePackageObject(ctx, pkg)
		db.Model(pkg).Updates(map[string]interface{}{"status": model.StoragePackageError, "upload_id": "",
			"reason": "the upload stopped for a day and was abandoned"})
	}
	verifying := []*model.StoragePackage{}
	db.Where("status = ? AND progress_at < ?", model.StoragePackageVerifying, time.Now().Add(-storagePackageVerifyStale)).Find(&verifying)
	for _, pkg := range verifying {
		now := time.Now()
		if db.Model(&model.StoragePackage{}).Where("id = ? AND status = ? AND progress_at = ?", pkg.ID, model.StoragePackageVerifying, pkg.ProgressAt).
			Update("progress_at", &now).RowsAffected == 1 {
			go verifyStoragePackage(context.WithoutCancel(ctx), pkg.ID)
		}
	}
}

// storagePackageDistro tells whether a package has packages for an Ubuntu release (VERSION_ID 24.04 -> ubuntu24)
func storagePackageDistro(pkg *model.StoragePackage, versionID string) bool {
	distros := []string{}
	_ = json.Unmarshal([]byte(pkg.Distros), &distros)
	want := "ubuntu" + strings.SplitN(versionID, ".", 2)[0]
	for _, d := range distros {
		if d == want {
			return true
		}
	}
	return false
}
