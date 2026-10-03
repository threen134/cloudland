package common

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

// HTTPError mirrors FastAPI's HTTPException: an HTTP status plus a "detail" payload.
type HTTPError struct {
	Status  int
	Detail  interface{}
	Headers map[string]string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %v", e.Status, e.Detail)
}

func NewHTTPError(status int, detail interface{}) *HTTPError {
	return &HTTPError{Status: status, Detail: detail}
}

// AbortWithError writes {"detail": ...} with the error's status and aborts the chain.
func AbortWithError(c *gin.Context, err *HTTPError) {
	for k, v := range err.Headers {
		c.Header(k, v)
	}
	c.AbortWithStatusJSON(err.Status, gin.H{"detail": err.Detail})
}

func AbortWithDetail(c *gin.Context, status int, detail interface{}) {
	c.AbortWithStatusJSON(status, gin.H{"detail": detail})
}

// AbortValidation mimics FastAPI's 422 request validation error body.
func AbortValidation(c *gin.Context, loc string, err error) {
	c.AbortWithStatusJSON(422, gin.H{"detail": []gin.H{{
		"loc":  []string{loc},
		"msg":  err.Error(),
		"type": "value_error",
	}}})
}
