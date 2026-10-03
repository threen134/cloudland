/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"encoding/json"
	"net/http"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var storagePackageAPI = &StoragePackageAPI{}
var storagePackageAdmin = services.StoragePackages

// StoragePackageAPI serves the storage package repository (shared-storage-design.md §6.1, §13.1). Every route is
// for system admins.
type StoragePackageAPI struct{}

type StoragePackagePayload struct {
	// A kind whose backend installs from a package (GET /storage_backends): gpfs
	Kind string `json:"kind" binding:"required,max=16"`
	// Upload in parts: the file name and size; or have clapi download it: url
	FileName  string `json:"file_name" binding:"omitempty,max=200"`
	SizeBytes int64  `json:"size_bytes" binding:"omitempty,min=1"`
	URL       string `json:"url" binding:"omitempty,max=1024"`
}

type StoragePackageResponse struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Edition   string `json:"edition,omitempty"`
	Version   string `json:"version,omitempty"`
	FileName  string `json:"file_name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty"`
	// Upload progress: parts are part_size bytes, the last one less; upload part parts_done+1 next
	PartSize   int64    `json:"part_size"`
	TotalParts int32    `json:"total_parts"`
	PartsDone  int32    `json:"parts_done"`
	SourceURL  string   `json:"source_url,omitempty"`
	Distros    []string `json:"distros"`
	// uploading | verifying | ready | error
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	AcceptedBy string `json:"accepted_by,omitempty"`
	AcceptedAt string `json:"accepted_at,omitempty"`
	CreatedAt  string `json:"created_at"`
	// Detail only: the license text by language (en, zh, zh_TW) and how many packages the manifest lists
	License       map[string]string `json:"license,omitempty"`
	ManifestCount int               `json:"manifest_count,omitempty"`
}

type StoragePackageListResponse struct {
	Offset          int                       `json:"offset"`
	Total           int                       `json:"total"`
	Limit           int                       `json:"limit"`
	StoragePackages []*StoragePackageResponse `json:"storage_packages"`
}

func storagePackageResponse(pkg *model.StoragePackage, detail bool) *StoragePackageResponse {
	resp := &StoragePackageResponse{ID: pkg.UUID, Kind: pkg.Kind, Edition: pkg.Edition, Version: pkg.Version, FileName: pkg.FileName,
		SizeBytes: pkg.SizeBytes, SHA256: pkg.SHA256, PartSize: services.StoragePackagePartSize, PartsDone: pkg.PartsDone,
		SourceURL: pkg.SourceURL, Distros: []string{}, Status: pkg.Status, Reason: pkg.Reason, AcceptedBy: pkg.AcceptedBy,
		AcceptedAt: formatTimePtr(pkg.AcceptedAt), CreatedAt: pkg.CreatedAt.Format(TimeStringForMat)}
	if pkg.SizeBytes > 0 {
		resp.TotalParts = int32((pkg.SizeBytes + services.StoragePackagePartSize - 1) / services.StoragePackagePartSize)
	}
	_ = json.Unmarshal([]byte(pkg.Distros), &resp.Distros)
	if detail {
		_ = json.Unmarshal([]byte(pkg.LicenseText), &resp.License)
		manifest := map[string]string{}
		_ = json.Unmarshal([]byte(pkg.Manifest), &manifest)
		resp.ManifestCount = len(manifest)
	}
	return resp
}

// @Summary list storage packages
// @tags StoragePackage
// @Produce json
// @Success 200 {object} StoragePackageListResponse
// @Router /storage_packages [get]
func (v *StoragePackageAPI) List(c *gin.Context) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if offset < 0 || limit < 0 || limit > 500 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", nil)
		return
	}
	total, pkgs, err := storagePackageAdmin.List(c.Request.Context(), int64(offset), int64(limit))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list storage packages", err)
		return
	}
	resp := &StoragePackageListResponse{Offset: offset, Total: int(total), Limit: len(pkgs), StoragePackages: []*StoragePackageResponse{}}
	for _, p := range pkgs {
		resp.StoragePackages = append(resp.StoragePackages, storagePackageResponse(p, false))
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary start a storage package
// @Description upload an installer in parts (file_name and size_bytes; then PUT the parts and POST complete), or have clapi download it (url). The package is verified in the background and needs its license accepted before a deployment can use it
// @tags StoragePackage
// @Accept  json
// @Produce json
// @Param   message  body  StoragePackagePayload  true  "Package"
// @Success 201 {object} StoragePackageResponse
// @Router /storage_packages [post]
func (v *StoragePackageAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &StoragePackagePayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	var pkg *model.StoragePackage
	var err error
	switch {
	case payload.URL != "" && payload.FileName == "" && payload.SizeBytes == 0:
		pkg, err = storagePackageAdmin.StartDownload(ctx, payload.Kind, payload.URL)
	case payload.URL == "" && payload.FileName != "" && payload.SizeBytes > 0:
		pkg, err = storagePackageAdmin.StartUpload(ctx, payload.Kind, payload.FileName, payload.SizeBytes)
	default:
		ErrorResponse(c, http.StatusBadRequest, "Give either file_name and size_bytes, or url", nil)
		return
	}
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to start the package", err)
		return
	}
	c.JSON(http.StatusCreated, storagePackageResponse(pkg, false))
}

// @Summary upload a part of a storage package
// @Description the body is the bytes of part n (from 1), part_size bytes but the last; parts go in order, and the last one may be sent again when its answer was lost
// @tags StoragePackage
// @Accept  application/octet-stream
// @Produce json
// @Param   id  path  string  true  "Package UUID"
// @Param   n   path  int     true  "Part number, from 1"
// @Success 200 {object} StoragePackageResponse
// @Router /storage_packages/{id}/parts/{n} [put]
func (v *StoragePackageAPI) UploadPart(c *gin.Context) {
	n, err := strconv.Atoi(c.Param("n"))
	if err != nil || n < 1 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid part number", err)
		return
	}
	if c.Request.ContentLength <= 0 || c.Request.ContentLength > services.StoragePackagePartSize {
		ErrorResponse(c, http.StatusBadRequest, "A part needs a Content-Length of at most the part size", nil)
		return
	}
	body := http.MaxBytesReader(c.Writer, c.Request.Body, services.StoragePackagePartSize)
	pkg, err := storagePackageAdmin.UploadPart(c.Request.Context(), c.Param("id"), int32(n), body, c.Request.ContentLength)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to upload the part", err)
		return
	}
	c.JSON(http.StatusOK, storagePackageResponse(pkg, false))
}

// @Summary complete the upload of a storage package
// @Description joins the parts once all are uploaded and starts the verification
// @tags StoragePackage
// @Produce json
// @Param   id  path  string  true  "Package UUID"
// @Success 202 {object} StoragePackageResponse
// @Router /storage_packages/{id}/complete [post]
func (v *StoragePackageAPI) Complete(c *gin.Context) {
	pkg, err := storagePackageAdmin.CompleteUpload(c.Request.Context(), c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to complete the upload", err)
		return
	}
	c.JSON(http.StatusAccepted, storagePackageResponse(pkg, false))
}

// @Summary get a storage package
// @Description with the license text and the number of packages its manifest lists
// @tags StoragePackage
// @Produce json
// @Param   id  path  string  true  "Package UUID"
// @Success 200 {object} StoragePackageResponse
// @Router /storage_packages/{id} [get]
func (v *StoragePackageAPI) Get(c *gin.Context) {
	pkg, err := storagePackageAdmin.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage package", err)
		return
	}
	c.JSON(http.StatusOK, storagePackageResponse(pkg, true))
}

// @Summary accept the license of a storage package
// @Description records who accepted it and when; a deployment can only use a package whose license is accepted
// @tags StoragePackage
// @Produce json
// @Param   id  path  string  true  "Package UUID"
// @Success 200 {object} StoragePackageResponse
// @Router /storage_packages/{id}/accept_license [post]
func (v *StoragePackageAPI) AcceptLicense(c *gin.Context) {
	pkg, err := storagePackageAdmin.AcceptLicense(c.Request.Context(), c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to accept the license", err)
		return
	}
	c.JSON(http.StatusOK, storagePackageResponse(pkg, false))
}

// @Summary delete a storage package
// @Description refused while a storage cluster uses it; an unfinished upload is aborted in S3
// @tags StoragePackage
// @Param   id  path  string  true  "Package UUID"
// @Success 204
// @Router /storage_packages/{id} [delete]
func (v *StoragePackageAPI) Delete(c *gin.Context) {
	if err := storagePackageAdmin.Delete(c.Request.Context(), c.Param("id")); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to delete the package", err)
		return
	}
	c.Status(http.StatusNoContent)
}
