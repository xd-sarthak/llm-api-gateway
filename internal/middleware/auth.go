package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"database/sql"
	"net/http"
	"strings"
	
)