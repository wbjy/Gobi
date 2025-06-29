package handlers

import (
	"encoding/json"
	"gobi/config"
	"gobi/internal/models"
	"gobi/pkg/database"
	"gobi/pkg/errors"
	"gobi/pkg/utils"
	"net/http"
	"time"

	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Auth handlers
func Login(c *gin.Context) {
	var login struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&login); err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "login",
			"username": login.Username,
			"error":    err.Error(),
		}).Warn("Login request validation failed")
		if errors.IsValidationError(err) {
			c.Error(errors.NewBadRequestError("Invalid login request", err))
		} else {
			c.Error(errors.WrapError(err, "Invalid login request"))
		}
		return
	}

	var user models.User
	if err := database.DB.Where("username = ?", login.Username).First(&user).Error; err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "login",
			"username": login.Username,
			"error":    "user not found",
		}).Warn("Login failed: user not found")
		c.Error(errors.ErrInvalidCredentials)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(login.Password)); err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "login",
			"username": login.Username,
			"error":    "invalid password",
		}).Warn("Login failed: invalid password")
		c.Error(errors.ErrInvalidCredentials)
		return
	}

	// 更新最后登录时间
	user.LastLogin = time.Now()
	database.DB.Save(&user)

	// cfg := config.DefaultConfig
	cfg := config.AppConfig
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": user.ID,
		"role":    user.Role,
		"exp":     time.Now().Add(time.Duration(cfg.JWT.ExpirationHours) * time.Hour).Unix(),
	})

	tokenString, err := token.SignedString([]byte(cfg.JWT.Secret))
	if err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "login",
			"username": login.Username,
			"error":    err.Error(),
		}).Error("Login failed: token generation error")
		c.Error(errors.WrapError(err, "Could not generate token"))
		return
	}

	utils.Logger.WithFields(map[string]interface{}{
		"action":   "login",
		"userID":   user.ID,
		"role":     user.Role,
		"username": user.Username,
	}).Info("User login success")

	c.JSON(http.StatusOK, gin.H{"token": tokenString})
}

// 从 Authorization 头解析 JWT 并获取 role
func getRoleFromToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return ""
	}
	tokenStr := parts[1]
	token, _ := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		return []byte(config.AppConfig.JWT.Secret), nil
	})
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if role, ok := claims["role"].(string); ok {
			return role
		}
	}
	return ""
}

func Register(c *gin.Context) {
	var register struct {
		Username string `json:"username" binding:"required"`
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
		IsAdmin  bool   `json:"is_admin"`
	}

	if err := c.ShouldBindJSON(&register); err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "register",
			"username": register.Username,
			"email":    register.Email,
			"error":    err.Error(),
		}).Warn("Register request validation failed")
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.IsValidationError(err) {
			c.Error(errors.NewBadRequestError("Invalid registration request", err))
		} else if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
			c.Error(errors.NewBadRequestError("Invalid JSON format", err))
		} else {
			c.Error(errors.WrapError(err, "Invalid registration request"))
		}
		return
	}

	// 检查是否已有 admin 用户
	var adminCount int64
	database.DB.Model(&models.User{}).Where("role = ?", "admin").Count(&adminCount)
	if adminCount > 0 {
		role := getRoleFromToken(c)
		if role != "admin" {
			utils.Logger.WithFields(map[string]interface{}{
				"action":   "register",
				"username": register.Username,
				"email":    register.Email,
				"error":    "only admin can register new users",
			}).Warn("Register forbidden: only admin can register new users")
			c.JSON(http.StatusForbidden, gin.H{"error": "Only admin can register new users"})
			return
		}
	}

	// 检查用户名或邮箱是否已存在
	var existingUser models.User
	err := database.DB.Where("username = ? OR email = ?", register.Username, register.Email).First(&existingUser).Error
	if err == nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "register",
			"username": register.Username,
			"email":    register.Email,
			"error":    "user or email exists",
		}).Warn("Register failed: user or email exists")
		c.Error(errors.NewConflictError("User or email already exists", nil))
		return
	} else if err != gorm.ErrRecordNotFound {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "register",
			"username": register.Username,
			"email":    register.Email,
			"error":    err.Error(),
		}).Error("Register failed: database error")
		c.Error(errors.WrapError(err, "Database error"))
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(register.Password), bcrypt.DefaultCost)
	if err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "register",
			"username": register.Username,
			"email":    register.Email,
			"error":    err.Error(),
		}).Error("Register failed: hash password error")
		c.Error(errors.WrapError(err, "Could not hash password"))
		return
	}

	roleStr := "user"
	if register.IsAdmin {
		roleStr = "admin"
	}

	user := models.User{
		Username: register.Username,
		Email:    register.Email,
		Password: string(hashedPassword),
		Role:     roleStr,
	}

	if err := database.DB.Create(&user).Error; err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "register",
			"username": register.Username,
			"email":    register.Email,
			"error":    err.Error(),
		}).Error("Register failed: create user error")
		c.Error(errors.WrapError(err, "Could not create user"))
		return
	}

	utils.Logger.WithFields(map[string]interface{}{
		"action":   "register",
		"userID":   user.ID,
		"role":     user.Role,
		"username": user.Username,
		"email":    user.Email,
	}).Info("User register success")

	c.JSON(http.StatusCreated, gin.H{"message": "User created successfully"})
}

// Query handlers
func CreateQuery(c *gin.Context) {
	var req struct {
		Name         string `json:"name"`
		SQL          string `json:"sql"`
		Description  string `json:"description"`
		IsPublic     bool   `json:"is_public"`
		DataSourceID uint   `json:"data_source_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action": "create_query",
			"userID": c.GetUint("userID"),
			"error":  err.Error(),
		}).Warn("Create query validation failed")
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.IsValidationError(err) {
			c.Error(errors.NewBadRequestError("Invalid query data", err))
		} else if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
			c.Error(errors.NewBadRequestError("Invalid JSON format", err))
		} else {
			c.Error(errors.WrapError(err, "Invalid query data"))
		}
		return
	}

	userID, _ := c.Get("userID")
	query := models.Query{
		Name:         req.Name,
		SQL:          req.SQL,
		Description:  req.Description,
		IsPublic:     req.IsPublic,
		DataSourceID: req.DataSourceID,
		UserID:       userID.(uint),
	}

	if err := database.DB.Create(&query).Error; err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action": "create_query",
			"userID": userID,
			"error":  err.Error(),
		}).Error("Create query failed: db error")
		c.Error(errors.WrapError(err, "Could not create query"))
		return
	}

	utils.QueryCache.Flush()

	utils.Logger.WithFields(map[string]interface{}{
		"action":  "create_query",
		"userID":  userID,
		"queryID": query.ID,
		"name":    query.Name,
	}).Info("Query created successfully")

	c.JSON(http.StatusCreated, query)
}

func ListQueries(c *gin.Context) {
	var queries []models.Query
	userID, _ := c.Get("userID")
	role, _ := c.Get("role")

	cacheKey := cacheKeyForListQueries(userID, role)
	if cached, found := utils.GetQueryCache(cacheKey); found {
		c.JSON(http.StatusOK, cached)
		return
	}

	query := database.DB.Model(&models.Query{})
	if role.(string) != "admin" {
		query = query.Where("user_id = ? OR is_public = ?", userID, true)
	}

	if err := query.Find(&queries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not fetch queries"})
		return
	}

	utils.SetQueryCache(cacheKey, queries, 5*time.Minute)
	c.JSON(http.StatusOK, queries)
}

func GetQuery(c *gin.Context) {
	id := c.Param("id")
	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	cacheKey := cacheKeyForGetQuery(id, userID, role)
	if cached, found := utils.GetQueryCache(cacheKey); found {
		c.JSON(http.StatusOK, cached)
		return
	}

	var query models.Query
	if err := database.DB.First(&query, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	if role.(string) != "admin" && query.UserID != userID.(uint) && !query.IsPublic {
		c.Error(errors.ErrForbidden)
		return
	}

	utils.SetQueryCache(cacheKey, query, 5*time.Minute)
	c.JSON(http.StatusOK, query)
}

func UpdateQuery(c *gin.Context) {
	id := c.Param("id")
	var query models.Query
	if err := database.DB.First(&query, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && query.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	var req struct {
		Name         string `json:"name"`
		SQL          string `json:"sql"`
		Description  string `json:"description"`
		IsPublic     bool   `json:"is_public"`
		DataSourceID uint   `json:"data_source_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.IsValidationError(err) {
			c.Error(errors.NewBadRequestError("Invalid query data", err))
		} else if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
			c.Error(errors.NewBadRequestError("Invalid JSON format", err))
		} else {
			c.Error(errors.WrapError(err, "Invalid query data"))
		}
		return
	}

	if req.Name != "" {
		query.Name = req.Name
	}
	if req.SQL != "" {
		query.SQL = req.SQL
	}
	if req.Description != "" {
		query.Description = req.Description
	}
	query.IsPublic = req.IsPublic
	if req.DataSourceID != 0 {
		query.DataSourceID = req.DataSourceID
	}

	if err := database.DB.Save(&query).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not update query"))
		return
	}

	utils.QueryCache.Flush()

	c.JSON(http.StatusOK, query)
}

func DeleteQuery(c *gin.Context) {
	id := c.Param("id")
	var query models.Query
	if err := database.DB.First(&query, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && query.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	if err := database.DB.Delete(&query).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not delete query"))
		return
	}

	utils.QueryCache.Flush()

	c.JSON(http.StatusOK, gin.H{"message": "Query deleted successfully"})
}

// Chart handlers
func CreateChart(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		QueryID     uint   `json:"query_id"`
		Config      string `json:"config"`
		Data        string `json:"data"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.IsValidationError(err) {
			c.Error(errors.NewBadRequestError("Invalid chart data", err))
		} else if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
			c.Error(errors.NewBadRequestError("Invalid JSON format", err))
		} else {
			c.Error(errors.WrapError(err, "Invalid chart data"))
		}
		return
	}

	// 验证图表类型
	validChartTypes := map[string]bool{
		"bar":        true,
		"line":       true,
		"pie":        true,
		"scatter":    true,
		"radar":      true,
		"heatmap":    true,
		"gauge":      true,
		"funnel":     true,
		"3d-bar":     true,
		"3d-scatter": true,
		"3d-surface": true,
		"3d-bubble":  true,
	}

	if !validChartTypes[req.Type] {
		c.Error(errors.NewBadRequestError("Invalid chart type", nil))
		return
	}

	userID, _ := c.Get("userID")
	chart := models.Chart{
		Name:        req.Name,
		Type:        req.Type,
		QueryID:     req.QueryID,
		Config:      req.Config,
		Data:        req.Data,
		Description: req.Description,
		UserID:      userID.(uint),
	}

	if err := database.DB.Create(&chart).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not create chart"))
		return
	}

	c.JSON(http.StatusCreated, chart)
}

func ListCharts(c *gin.Context) {
	var charts []models.Chart
	userID, _ := c.Get("userID")
	role, _ := c.Get("role")

	query := database.DB.Preload("Query").Preload("User").Model(&models.Chart{})
	if role.(string) != "admin" {
		query = query.Where("user_id = ?", userID)
	}

	if err := query.Find(&charts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not fetch charts"})
		return
	}

	c.JSON(http.StatusOK, charts)
}

func GetChart(c *gin.Context) {
	id := c.Param("id")
	var chart models.Chart
	if err := database.DB.Preload("Query").Preload("User").First(&chart, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && chart.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	c.JSON(http.StatusOK, chart)
}

func UpdateChart(c *gin.Context) {
	id := c.Param("id")
	var chart models.Chart
	if err := database.DB.First(&chart, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && chart.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	if err := c.ShouldBindJSON(&chart); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.IsValidationError(err) {
			c.Error(errors.NewBadRequestError("Invalid chart data", err))
		} else if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
			c.Error(errors.NewBadRequestError("Invalid JSON format", err))
		} else {
			c.Error(errors.WrapError(err, "Invalid chart data"))
		}
		return
	}

	if err := database.DB.Save(&chart).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not update chart"))
		return
	}

	c.JSON(http.StatusOK, chart)
}

func DeleteChart(c *gin.Context) {
	id := c.Param("id")
	var chart models.Chart
	if err := database.DB.First(&chart, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && chart.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	if err := database.DB.Delete(&chart).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not delete chart"))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Chart deleted successfully"})
}

// Excel template handlers
func CreateTemplate(c *gin.Context) {
	file, err := c.FormFile("template")
	if err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action": "create_template",
			"userID": c.GetUint("userID"),
			"error":  err.Error(),
		}).Warn("Create template: no file uploaded")
		if errors.IsContentTypeError(err) {
			c.Error(errors.NewBadRequestError("No file uploaded", err))
		} else {
			c.Error(errors.WrapError(err, "No file uploaded"))
		}
		return
	}
	openedFile, err := file.Open()
	if err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action": "create_template",
			"userID": c.GetUint("userID"),
			"error":  err.Error(),
		}).Error("Create template: open file failed")
		c.Error(errors.WrapError(err, "Could not open file"))
		return
	}
	defer openedFile.Close()
	desc := c.PostForm("description")
	template := models.ExcelTemplate{
		Name:        file.Filename,
		UserID:      c.GetUint("userID"),
		Template:    make([]byte, file.Size),
		Description: desc,
	}
	if file.Size > 0 {
		if _, err := openedFile.Read(template.Template); err != nil && err.Error() != "EOF" {
			utils.Logger.WithFields(map[string]interface{}{
				"action": "create_template",
				"userID": c.GetUint("userID"),
				"error":  err.Error(),
			}).Error("Create template: read file failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not read file: " + err.Error()})
			return
		}
	}
	if err := database.DB.Create(&template).Error; err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action": "create_template",
			"userID": c.GetUint("userID"),
			"error":  err.Error(),
		}).Error("Create template: db error")
		c.Error(errors.WrapError(err, "Could not save template"))
		return
	}
	utils.Logger.WithFields(map[string]interface{}{
		"action":     "create_template",
		"userID":     c.GetUint("userID"),
		"templateID": template.ID,
		"name":       template.Name,
	}).Info("Template created successfully")
	c.JSON(http.StatusCreated, template)
}

func ListTemplates(c *gin.Context) {
	var templates []models.ExcelTemplate
	userID, _ := c.Get("userID")
	role, _ := c.Get("role")

	query := database.DB.Model(&models.ExcelTemplate{})
	if role.(string) != "admin" {
		query = query.Where("user_id = ?", userID)
	}

	if err := query.Find(&templates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not fetch templates"})
		return
	}

	// 返回时包含 description 字段
	result := make([]map[string]interface{}, 0, len(templates))
	for _, t := range templates {
		result = append(result, map[string]interface{}{
			"id":          t.ID,
			"name":        t.Name,
			"user_id":     t.UserID,
			"created_at":  t.CreatedAt,
			"description": t.Description,
		})
	}
	c.JSON(http.StatusOK, result)
}

func GetTemplate(c *gin.Context) {
	id := c.Param("id")
	var template models.ExcelTemplate
	if err := database.DB.First(&template, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && template.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"id":          template.ID,
		"name":        template.Name,
		"user_id":     template.UserID,
		"created_at":  template.CreatedAt,
		"description": template.Description,
	})
}

func UpdateTemplate(c *gin.Context) {
	id := c.Param("id")
	var template models.ExcelTemplate
	if err := database.DB.First(&template, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	if template.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	file, err := c.FormFile("template")
	if err != nil {
		if errors.IsContentTypeError(err) {
			c.Error(errors.NewBadRequestError("No file uploaded", err))
		} else {
			c.Error(errors.WrapError(err, "No file uploaded"))
		}
		return
	}

	openedFile, err := file.Open()
	if err != nil {
		c.Error(errors.WrapError(err, "Could not open file"))
		return
	}
	defer openedFile.Close()

	template.Name = file.Filename
	template.Template = make([]byte, file.Size)
	if _, err := openedFile.Read(template.Template); err != nil {
		c.Error(errors.WrapError(err, "Could not read file"))
		return
	}

	if err := database.DB.Save(&template).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not update template"))
		return
	}

	c.JSON(http.StatusOK, template)
}

func DeleteTemplate(c *gin.Context) {
	id := c.Param("id")
	var template models.ExcelTemplate
	if err := database.DB.First(&template, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")

	// 权限检查：管理员可以删除所有模板，普通用户只能删除自己的模板
	if role.(string) != "admin" && template.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	if err := database.DB.Delete(&template).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not delete template"))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Template deleted successfully"})
}

// DownloadTemplate 下载模板文件
func DownloadTemplate(c *gin.Context) {
	id := c.Param("id")
	var template models.ExcelTemplate
	if err := database.DB.First(&template, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")

	// 权限检查：管理员可以下载所有模板，普通用户只能下载自己的模板
	if role.(string) != "admin" && template.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	// 设置响应头，告诉浏览器这是一个文件下载
	c.Header("Content-Disposition", "attachment; filename="+template.Name)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Length", fmt.Sprintf("%d", len(template.Template)))

	// 写入文件内容
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", template.Template)
}

// DataSource handlers
func CreateDataSource(c *gin.Context) {
	var dataSource models.DataSource
	if err := c.ShouldBindJSON(&dataSource); err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action": "create_datasource",
			"userID": c.GetUint("userID"),
			"error":  err.Error(),
		}).Warn("Create datasource validation failed")
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.IsValidationError(err) {
			c.Error(errors.NewBadRequestError("Invalid data source data", err))
		} else if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
			c.Error(errors.NewBadRequestError("Invalid JSON format", err))
		} else {
			c.Error(errors.WrapError(err, "Invalid data source data"))
		}
		return
	}
	userID, _ := c.Get("userID")
	dataSource.UserID = userID.(uint)
	// 加密密码
	if dataSource.Password != "" {
		encrypted, err := utils.EncryptAES(dataSource.Password)
		if err != nil {
			utils.Logger.WithFields(map[string]interface{}{
				"action": "create_datasource",
				"userID": userID,
				"error":  err.Error(),
			}).Error("Create datasource: encrypt password error")
			c.Error(errors.WrapError(err, "Could not encrypt password"))
			return
		}
		dataSource.Password = encrypted
	}
	if err := database.DB.Create(&dataSource).Error; err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action": "create_datasource",
			"userID": userID,
			"error":  err.Error(),
		}).Error("Create datasource: db error")
		c.Error(errors.WrapError(err, "Could not create data source"))
		return
	}
	utils.QueryCache.Flush()
	utils.Logger.WithFields(map[string]interface{}{
		"action":       "create_datasource",
		"userID":       userID,
		"datasourceID": dataSource.ID,
		"name":         dataSource.Name,
	}).Info("Datasource created successfully")
	c.JSON(http.StatusCreated, dataSource)
}

func ListDataSources(c *gin.Context) {
	var dataSources []models.DataSource
	userID, _ := c.Get("userID")
	role, _ := c.Get("role")

	query := database.DB.Model(&models.DataSource{})
	if role.(string) != "admin" {
		query = query.Where("user_id = ? OR is_public = ?", userID, true)
	}

	if err := query.Find(&dataSources).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not fetch data sources"))
		return
	}

	// 清除密码字段
	for i := range dataSources {
		dataSources[i].Password = ""
	}

	c.JSON(http.StatusOK, dataSources)
}

func GetDataSource(c *gin.Context) {
	id := c.Param("id")
	var dataSource models.DataSource
	if err := database.DB.First(&dataSource, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && dataSource.UserID != userID.(uint) && !dataSource.IsPublic {
		c.Error(errors.ErrForbidden)
		return
	}

	// 清除密码字段
	dataSource.Password = ""

	c.JSON(http.StatusOK, dataSource)
}

func UpdateDataSource(c *gin.Context) {
	id := c.Param("id")
	var dataSource models.DataSource
	if err := database.DB.First(&dataSource, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && dataSource.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	var updateData struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		Database    string `json:"database"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		Description string `json:"description"`
		IsPublic    bool   `json:"isPublic"`
	}

	if err := c.ShouldBindJSON(&updateData); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.IsValidationError(err) {
			c.Error(errors.NewBadRequestError("Invalid data source data", err))
		} else if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
			c.Error(errors.NewBadRequestError("Invalid JSON format", err))
		} else {
			c.Error(errors.WrapError(err, "Invalid data source data"))
		}
		return
	}

	// 更新字段
	dataSource.Name = updateData.Name
	dataSource.Type = updateData.Type
	dataSource.Host = updateData.Host
	dataSource.Port = updateData.Port
	dataSource.Database = updateData.Database
	dataSource.Username = updateData.Username
	dataSource.Description = updateData.Description
	dataSource.IsPublic = updateData.IsPublic

	// 如果提供了新密码，则加密
	if updateData.Password != "" {
		encrypted, err := utils.EncryptAES(updateData.Password)
		if err != nil {
			c.Error(errors.WrapError(err, "Could not encrypt password"))
			return
		}
		dataSource.Password = encrypted
	}

	if err := database.DB.Save(&dataSource).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not update data source"))
		return
	}

	utils.QueryCache.Flush()

	// 清除密码字段
	dataSource.Password = ""

	c.JSON(http.StatusOK, dataSource)
}

func DeleteDataSource(c *gin.Context) {
	id := c.Param("id")
	var dataSource models.DataSource
	if err := database.DB.First(&dataSource, id).Error; err != nil {
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && dataSource.UserID != userID.(uint) {
		c.Error(errors.ErrForbidden)
		return
	}

	// 检查是否有查询使用此数据源
	var count int64
	if err := database.DB.Model(&models.Query{}).Where("data_source_id = ?", id).Count(&count).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not check data source usage"))
		return
	}

	if count > 0 {
		c.Error(errors.NewBadRequestError("Cannot delete data source that is being used by queries", nil))
		return
	}

	if err := database.DB.Delete(&dataSource).Error; err != nil {
		c.Error(errors.WrapError(err, "Could not delete data source"))
		return
	}

	utils.QueryCache.Flush()

	c.JSON(http.StatusOK, gin.H{"message": "Data source deleted successfully"})
}

// 辅助函数：加密密码
func encryptPassword(password string) (string, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashedPassword), nil
}

func cacheKeyForListQueries(userID interface{}, role interface{}) string {
	key := "list_queries:" + toString(userID) + ":" + toString(role)
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func cacheKeyForGetQuery(id string, userID interface{}, role interface{}) string {
	key := "get_query:" + id + ":" + toString(userID) + ":" + toString(role)
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func toString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return fmt.Sprintf("%d", t)
	case uint:
		return fmt.Sprintf("%d", t)
	default:
		return ""
	}
}

// 管理员手动清理缓存接口
func ClearCache(c *gin.Context) {
	role, _ := c.Get("role")
	if role.(string) != "admin" {
		c.Error(errors.ErrForbidden)
		return
	}
	var req struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError("Invalid request", err))
		return
	}
	switch req.Type {
	case "all":
		utils.QueryCache.Flush()
	case "query":
		if req.ID != "" {
			for k := range utils.QueryCache.Items() {
				if len(k) > 9 && k[0:9] == "get_query:" && k[10:10+len(req.ID)] == req.ID {
					utils.QueryCache.Delete(k)
				}
			}
		}
	case "list":
		for k := range utils.QueryCache.Items() {
			if len(k) > 12 && k[0:12] == "list_queries" {
				utils.QueryCache.Delete(k)
			}
		}
	default:
		utils.QueryCache.Flush()
	}
	c.JSON(http.StatusOK, gin.H{"message": "Cache cleared"})
}

// Dashboard stats handler
func DashboardStats(c *gin.Context) {
	var totalQueries int64
	var totalCharts int64
	var totalUsers int64
	var todayQueries int64

	today := time.Now().Format("2006-01-02")
	database.DB.Model(&models.Query{}).Count(&totalQueries)
	database.DB.Model(&models.Chart{}).Count(&totalCharts)
	database.DB.Model(&models.User{}).Count(&totalUsers)
	database.DB.Model(&models.Query{}).Where("DATE(created_at) = ?", today).Count(&todayQueries)

	// 查询趋势（最近7天每天的查询数）
	queryTrends := []map[string]interface{}{}
	for i := 6; i >= 0; i-- {
		date := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		var count int64
		database.DB.Model(&models.Query{}).Where("DATE(created_at) = ?", date).Count(&count)
		queryTrends = append(queryTrends, map[string]interface{}{"date": date, "count": count})
	}

	// 热门查询（执行次数最多的前5个查询）
	type HotQuery struct {
		Name  string
		Count int64
	}
	hotQueries := []HotQuery{}
	database.DB.Table("queries").Select("name, exec_count as count").Order("exec_count desc").Limit(5).Scan(&hotQueries)

	c.JSON(http.StatusOK, gin.H{
		"totalQueries": totalQueries,
		"totalCharts":  totalCharts,
		"totalUsers":   totalUsers,
		"todayQueries": todayQueries,
		"queryTrends":  queryTrends,
		"hotQueries":   hotQueries,
	})
}

// List users handler
func ListUsers(c *gin.Context) {
	role, _ := c.Get("role")
	userID, _ := c.Get("userID")
	var dbUsers []models.User

	if role.(string) == "admin" {
		if err := database.DB.Find(&dbUsers).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not fetch users"})
			return
		}
	} else {
		if err := database.DB.Where("id = ?", userID).Find(&dbUsers).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not fetch user"})
			return
		}
	}

	var users []struct {
		ID        uint      `json:"id"`
		Username  string    `json:"username"`
		Email     string    `json:"email"`
		Role      string    `json:"role"`
		CreatedAt time.Time `json:"created_at"`
		LastLogin time.Time `json:"last_login"`
	}
	for _, u := range dbUsers {
		users = append(users, struct {
			ID        uint      `json:"id"`
			Username  string    `json:"username"`
			Email     string    `json:"email"`
			Role      string    `json:"role"`
			CreatedAt time.Time `json:"created_at"`
			LastLogin time.Time `json:"last_login"`
		}{
			ID:        u.ID,
			Username:  u.Username,
			Email:     u.Email,
			Role:      u.Role,
			CreatedAt: u.CreatedAt,
			LastLogin: u.LastLogin,
		})
	}
	c.JSON(http.StatusOK, users)
}

// Update user handler
func UpdateUser(c *gin.Context) {
	id := c.Param("id")
	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && toString(userID) != id {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "update_user",
			"userID":   userID,
			"targetID": id,
			"role":     role,
			"error":    "forbidden (only admin or self)"}).Warn("Update user: forbidden (only admin or self)")
		c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
		return
	}
	var user models.User
	if err := database.DB.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	if req.Username != "" {
		user.Username = req.Username
	}
	if req.Email != "" {
		user.Email = req.Email
	}
	if req.Role != "" {
		if role.(string) != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Only admin can change role"})
			return
		}
		user.Role = req.Role
	}
	if req.Password != "" {
		hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not hash password"})
			return
		}
		user.Password = string(hashed)
	}
	if err := database.DB.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not update user"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "User updated successfully"})
}

// Reset user password handler
func ResetUserPassword(c *gin.Context) {
	id := c.Param("id")
	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && toString(userID) != id {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "reset_password",
			"userID":   userID,
			"targetID": id,
			"role":     role,
			"error":    "forbidden (only admin or self)"}).Warn("Reset password: forbidden (only admin or self)")
		c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
		return
	}
	var user models.User
	if err := database.DB.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password is required"})
		return
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not hash password"})
		return
	}
	user.Password = string(hashed)
	if err := database.DB.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not reset password"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Password reset successfully"})
}

// Execute query handler
func ExecuteQuery(c *gin.Context) {
	id := c.Param("id")
	var query models.Query
	if err := database.DB.Preload("DataSource").First(&query, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Query not found"})
		return
	}
	// 权限校验：仅本人或公开或管理员可执行
	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" && query.UserID != userID.(uint) && !query.IsPublic {
		c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
		return
	}
	// 连接数据源并执行 SQL
	// 解密密码
	if query.DataSource.Password != "" {
		pwd, err := utils.DecryptAES(query.DataSource.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not decrypt data source password"})
			return
		}
		query.DataSource.Password = pwd
	}
	result, err := utils.ExecuteSQL(query.DataSource, query.SQL)
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 执行次数+1
	query.ExecCount++
	database.DB.Save(&query)
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func DeleteUser(c *gin.Context) {
	id := c.Param("id")
	var user models.User
	if err := database.DB.First(&user, id).Error; err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "delete_user",
			"targetID": id,
			"error":    "user not found",
		}).Warn("Delete user: not found")
		c.Error(errors.ErrNotFound)
		return
	}

	userID, _ := c.Get("userID")
	role, _ := c.Get("role")
	if role.(string) != "admin" {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "delete_user",
			"userID":   userID,
			"targetID": user.ID,
			"role":     role,
			"error":    "forbidden (only admin can delete)",
		}).Warn("Delete user: forbidden (only admin)")
		c.Error(errors.ErrForbidden)
		return
	}

	if err := database.DB.Delete(&user).Error; err != nil {
		utils.Logger.WithFields(map[string]interface{}{
			"action":   "delete_user",
			"userID":   userID,
			"targetID": user.ID,
			"role":     role,
			"error":    err.Error(),
		}).Error("Delete user: db error")
		c.Error(errors.WrapError(err, "Could not delete user"))
		return
	}

	utils.Logger.WithFields(map[string]interface{}{
		"action":   "delete_user",
		"userID":   userID,
		"targetID": user.ID,
		"role":     role,
	}).Info("User deleted")

	c.JSON(http.StatusOK, gin.H{"message": "User deleted successfully"})
}
