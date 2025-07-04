package main

import (
	"gobi/config"
	"gobi/internal/handlers"
	"gobi/internal/middleware"
	"gobi/pkg/database"
	"gobi/pkg/utils"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	limiterlib "github.com/ulule/limiter/v3"
	ginlimiter "github.com/ulule/limiter/v3/drivers/middleware/gin"
	memory "github.com/ulule/limiter/v3/drivers/store/memory"
	ginprometheus "github.com/zsais/go-gin-prometheus"
)

func main() {
	// 自动加载 .env 文件
	_ = godotenv.Load()

	// 加载 config.yaml 配置
	config.LoadConfig()
	cfg := config.AppConfig

	// Initialize database
	if err := database.InitDB(&cfg); err != nil {
		utils.Logger.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize query cache (default 5 min, cleanup 10 min)
	utils.InitQueryCache(5*time.Minute, 10*time.Minute)

	// Create Gin router
	r := gin.New()

	// CORS 中间件
	r.Use(cors.New(cors.Config{
		AllowOrigins: []string{
			"http://localhost:5173",
			"http://127.0.0.1:5173",
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Authorization", "Content-Type", "Accept", "X-Requested-With"},
		ExposeHeaders:    []string{"Content-Length", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// API 限流中间件（全局，10 req/s）
	rate, _ := limiterlib.NewRateFromFormatted("10-S")
	store := memory.NewStore()
	instance := ginlimiter.NewMiddleware(limiterlib.New(store, rate))
	r.Use(instance)

	// Prometheus metrics
	p := ginprometheus.NewPrometheus("gobi")
	p.Use(r)

	// 健康检查接口
	r.GET("/healthz", func(c *gin.Context) {
		sqlDB, err := database.DB.DB()
		if err != nil || sqlDB.Ping() != nil {
			c.JSON(500, gin.H{"status": "db error"})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Add middleware
	r.Use(middleware.Recovery())
	r.Use(middleware.ErrorHandler())
	r.Use(gin.Logger())

	// Public routes
	r.POST("/api/auth/login", handlers.Login)
	r.POST("/api/auth/register", handlers.Register)

	// Protected routes
	authorized := r.Group("/api")
	authorized.Use(middleware.AuthMiddleware(&cfg))
	{
		// Query routes
		authorized.POST("/queries", handlers.CreateQuery)
		authorized.GET("/queries", handlers.ListQueries)
		authorized.GET("/queries/:id", handlers.GetQuery)
		authorized.PUT("/queries/:id", handlers.UpdateQuery)
		authorized.DELETE("/queries/:id", handlers.DeleteQuery)
		authorized.POST("/queries/:id/execute", handlers.ExecuteQuery)

		// Data source routes
		authorized.POST("/datasources", handlers.CreateDataSource)
		authorized.GET("/datasources", handlers.ListDataSources)
		authorized.GET("/datasources/:id", handlers.GetDataSource)
		authorized.PUT("/datasources/:id", handlers.UpdateDataSource)
		authorized.DELETE("/datasources/:id", handlers.DeleteDataSource)

		// Chart routes
		authorized.POST("/charts", handlers.CreateChart)
		authorized.GET("/charts", handlers.ListCharts)
		authorized.GET("/charts/:id", handlers.GetChart)
		authorized.PUT("/charts/:id", handlers.UpdateChart)
		authorized.DELETE("/charts/:id", handlers.DeleteChart)

		// Excel template routes
		authorized.POST("/templates", handlers.CreateTemplate)
		authorized.GET("/templates", handlers.ListTemplates)
		authorized.GET("/templates/:id", handlers.GetTemplate)
		authorized.GET("/templates/:id/download", handlers.DownloadTemplate)
		authorized.PUT("/templates/:id", handlers.UpdateTemplate)
		authorized.DELETE("/templates/:id", handlers.DeleteTemplate)

		// Cache clear (admin only)
		authorized.POST("/cache/clear", handlers.ClearCache)

		// Dashboard stats
		authorized.GET("/dashboard/stats", handlers.DashboardStats)

		// User list
		authorized.GET("/users", handlers.ListUsers)
		// User update
		authorized.PUT("/users/:id", handlers.UpdateUser)
		// User reset password
		authorized.POST("/users/:id/reset-password", handlers.ResetUserPassword)
		// User delete
		authorized.DELETE("/users/:id", handlers.DeleteUser)

		// Report schedule routes
		authorized.POST("/reports/schedules", handlers.CreateReportSchedule)
		authorized.GET("/reports/schedules", handlers.ListReportSchedules)
		authorized.GET("/reports/schedules/:id", handlers.GetReportSchedule)
		authorized.PUT("/reports/schedules/:id", handlers.UpdateReportSchedule)
		authorized.DELETE("/reports/schedules/:id", handlers.DeleteReportSchedule)

		// Report routes
		authorized.GET("/reports", handlers.ListReports)
		authorized.GET("/reports/:id/download", handlers.DownloadReport)
	}

	// Initialize report generator
	utils.InitReportGenerator()
	defer utils.StopReportGenerator()

	// Start server
	if err := r.Run(":" + cfg.Server.Port); err != nil {
		utils.Logger.Fatalf("Failed to start server: %v", err)
	}
}
