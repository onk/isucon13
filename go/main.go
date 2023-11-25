package main

// ISUCON的な参考: https://github.com/isucon/isucon12-qualify/blob/main/webapp/go/isuports.go#L336
// sqlx的な参考: https://jmoiron.github.io/sqlx/
import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"strconv"

	"github.com/go-sql-driver/mysql"
	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echolog "github.com/labstack/gommon/log"
	"github.com/redis/go-redis/v9"
)

const (
	listenPort                     = 8080
	powerDNSSubdomainAddressEnvKey = "ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS"
)

var (
	powerDNSSubdomainAddress string
	dbConn                   *sqlx.DB
	secret                   = []byte("isucon13_session_cookiestore_defaultsecret")
)

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	if secretKey, ok := os.LookupEnv("ISUCON13_SESSION_SECRETKEY"); ok {
		secret = []byte(secretKey)
	}
}

type InitializeResponse struct {
	Language string `json:"language"`
}

func connectDB(logger echo.Logger) (*sqlx.DB, error) {
	const (
		networkTypeEnvKey = "ISUCON13_MYSQL_DIALCONFIG_NET"
		addrEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_ADDRESS"
		portEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_PORT"
		userEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_USER"
		passwordEnvKey    = "ISUCON13_MYSQL_DIALCONFIG_PASSWORD"
		dbNameEnvKey      = "ISUCON13_MYSQL_DIALCONFIG_DATABASE"
		parseTimeEnvKey   = "ISUCON13_MYSQL_DIALCONFIG_PARSETIME"
	)

	conf := mysql.NewConfig()

	// 環境変数がセットされていなかった場合でも一旦動かせるように、デフォルト値を入れておく
	// この挙動を変更して、エラーを出すようにしてもいいかもしれない
	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort("127.0.0.1", "3306")
	conf.User = "isucon"
	conf.Passwd = "isucon"
	conf.DBName = "isupipe"
	conf.ParseTime = true
	conf.Params = map[string]string{"interpolateParams": "true"}

	if v, ok := os.LookupEnv(networkTypeEnvKey); ok {
		conf.Net = v
	}
	if addr, ok := os.LookupEnv(addrEnvKey); ok {
		if port, ok2 := os.LookupEnv(portEnvKey); ok2 {
			conf.Addr = net.JoinHostPort(addr, port)
		} else {
			conf.Addr = net.JoinHostPort(addr, "3306")
		}
	}
	if v, ok := os.LookupEnv(userEnvKey); ok {
		conf.User = v
	}
	if v, ok := os.LookupEnv(passwordEnvKey); ok {
		conf.Passwd = v
	}
	if v, ok := os.LookupEnv(dbNameEnvKey); ok {
		conf.DBName = v
	}
	if v, ok := os.LookupEnv(parseTimeEnvKey); ok {
		parseTime, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("failed to parse environment variable '%s' as bool: %+v", parseTimeEnvKey, err)
		}
		conf.ParseTime = parseTime
	}

	db, err := sqlx.Open("mysql", conf.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

func initializeHandler(c echo.Context) error {
	go func() {
		_ = redisClient.FlushAll(context.Background()).Err()
	}()

	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		c.Logger().Warnf("init.sh failed with err=%s", string(out))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to initialize: "+err.Error())
	}

	cacheTagsOnInit()
	cacheLivestreamTagsOnInit()
	cacheTipsOnInit()
	cacheLivestreamViewersHistoryOnInit()
	cacheReactionsOnInit()
	cacheSpamCountOnInit()

	c.Request().Header.Add("Content-Type", "application/json;charset=utf-8")
	return c.JSON(http.StatusOK, InitializeResponse{
		Language: "golang",
	})
}

const tagsCacheRedisKey = "tags"
const TagID2NameCacheRedisKeyPrefix = "tag_id2name:"
const Name2TagIDCacheRedisKeyPrefix = "name2tag_id:"

func cacheTagsOnInit() {
	var tagModels []*TagModel
	err := dbConn.Select(&tagModels, "SELECT * FROM tags")
	if err != nil {
		log.Fatalf("failed to cache the tags: %s", err)
	}

	tagsCacheItems := make([]interface{}, 0, len(tagModels))
	tagID2NameCacheItems := make([]interface{}, len(tagModels)*2)
	name2tagIDCacheItems := make([]interface{}, len(tagModels)*2)
	i := 0
	for _, tag := range tagModels {
		tagID2NameCacheItems[i] = fmt.Sprintf("%s%d", TagID2NameCacheRedisKeyPrefix, tag.ID)
		name2tagIDCacheItems[i] = Name2TagIDCacheRedisKeyPrefix + tag.Name
		i++
		tagID2NameCacheItems[i] = tag.Name
		name2tagIDCacheItems[i] = strconv.FormatInt(tag.ID, 10)
		i++

		tagsCacheItems = append(tagsCacheItems, fmt.Sprintf("%d:%s", tag.ID, tag.Name))
	}
	err = redisClient.MSet(context.Background(), tagID2NameCacheItems...).Err()
	if err != nil {
		log.Fatalf("failed to make cache for tags: %w", err)
	}
	err = redisClient.MSet(context.Background(), name2tagIDCacheItems...).Err()
	if err != nil {
		log.Fatalf("failed to make cache for tags: %w", err)
	}

	redisClient.LPush(context.Background(), tagsCacheRedisKey, tagsCacheItems...)
}

func cacheLivestreamTagsOnInit() {
	var keyTaggedLivestreams []*LivestreamTagModel
	err := dbConn.Select(&keyTaggedLivestreams, "SELECT * FROM livestream_tags")
	if err != nil {
		log.Fatalf("failed to cache the livestream_tags: %s", err)
	}

	livestreamID2TagIDs := make(map[int64][]interface{})
	for _, livestreamTag := range keyTaggedLivestreams {
		if livestreamID2TagIDs[livestreamTag.LivestreamID] == nil {
			livestreamID2TagIDs[livestreamTag.LivestreamID] = make([]interface{}, 0)
		}
		livestreamID2TagIDs[livestreamTag.LivestreamID] = append(livestreamID2TagIDs[livestreamTag.LivestreamID], strconv.FormatInt(livestreamTag.TagID, 10))
	}

	for livestreamID, tagIDs := range livestreamID2TagIDs {
		err = redisClient.LPush(context.Background(), fmt.Sprintf("%s%d", LivestreamTagsCacheRedisKeyPrefix, livestreamID), tagIDs...).Err()
		if err != nil {
			log.Fatalf("failed to cache the livestream_tags: %s", err)
		}
	}
}

const LiveCommentTipsCacheRedisKeyPrefix = "live_comment_tips:"

func cacheTipsOnInit() {
	var liveComments []*LivecommentModel
	err := dbConn.Select(&liveComments, "SELECT * FROM livecomments")
	if err != nil {
		log.Fatalf("failed to cache the livecomment: %s", err)
	}

	cacheItems := make([]interface{}, len(liveComments)*2)
	i := 0
	for _, comment := range liveComments {
		cacheItems[i] = fmt.Sprintf("%s%d:%d", LiveCommentTipsCacheRedisKeyPrefix, comment.LivestreamID, comment.ID)
		i++
		cacheItems[i] = strconv.FormatInt(comment.Tip, 10)
		i++
	}

	err = redisClient.MSet(context.Background(), cacheItems...).Err()
	if err != nil {
		log.Fatalf("failed to cache the livecomment: %s", err)
	}
}

const ViewersCountCachePrefix = "num_viewers:"

func cacheLivestreamViewersHistoryOnInit() {
	var livestreamViewers []*LivestreamViewerModel
	err := dbConn.Select(&livestreamViewers, "SELECT * FROM livestream_viewers_history")
	if err != nil {
		log.Fatalf("failed to cache the livestreamViewers: %s", err)
	}

	for _, viewer := range livestreamViewers {
		err := redisClient.Incr(context.Background(), fmt.Sprintf("%s%d", ViewersCountCachePrefix, viewer.LivestreamID)).Err()
		if err != nil {
			log.Fatalf("failed to cache the livestreamViewers: %s", err)
		}
	}
}

const reactionsCachePrefix = "num_reactions:"

func cacheReactionsOnInit() {
	var reactions []*ReactionModel
	err := dbConn.Select(&reactions, "SELECT * FROM reactions")
	if err != nil {
		log.Fatalf("failed to cache the livestreamViewers: %s", err)
	}

	for _, reaction := range reactions {
		err := redisClient.Incr(context.Background(), fmt.Sprintf("%s%d", reactionsCachePrefix, reaction.LivestreamID)).Err()
		if err != nil {
			log.Fatalf("failed to cache the livestreamViewers: %s", err)
		}
	}
}

const spamCountCachePrefix = "num_spam_report:"

func cacheSpamCountOnInit() {
	var reports []*LivecommentReportModel
	err := dbConn.Select(&reports, "SELECT * FROM livecomment_reports")
	if err != nil {
		log.Fatalf("failed to cache the livecommentreport: %s", err)
	}

	for _, report := range reports {
		err := redisClient.Incr(context.Background(), fmt.Sprintf("%s%d", spamCountCachePrefix, report.LivestreamID)).Err()
		if err != nil {
			log.Fatalf("failed to cache the livecommentreport: %s", err)
		}
	}
}

var redisClient *redis.Client

func main() {
	redisHost := os.Getenv("REDIS_HOST")
	if redisHost == "" {
		redisHost = "127.0.0.1"
	}
	redisClient = redis.NewClient(&redis.Options{
		Addr: redisHost + ":6379",
	})

	e := echo.New()
	e.Debug = true
	e.Logger.SetLevel(echolog.DEBUG)
	e.Use(middleware.Logger())
	cookieStore := sessions.NewCookieStore(secret)
	cookieStore.Options.Domain = "*.u.isucon.dev"
	e.Use(session.Middleware(cookieStore))
	// e.Use(middleware.Recover())

	// 初期化
	e.POST("/api/initialize", initializeHandler)

	// top
	e.GET("/api/tag", getTagHandler)
	e.GET("/api/user/:username/theme", getStreamerThemeHandler)

	// livestream
	// reserve livestream
	e.POST("/api/livestream/reservation", reserveLivestreamHandler)
	// list livestream
	e.GET("/api/livestream/search", searchLivestreamsHandler)
	e.GET("/api/livestream", getMyLivestreamsHandler)
	e.GET("/api/user/:username/livestream", getUserLivestreamsHandler)
	// get livestream
	e.GET("/api/livestream/:livestream_id", getLivestreamHandler)
	// get polling livecomment timeline
	e.GET("/api/livestream/:livestream_id/livecomment", getLivecommentsHandler)
	// ライブコメント投稿
	e.POST("/api/livestream/:livestream_id/livecomment", postLivecommentHandler)
	e.POST("/api/livestream/:livestream_id/reaction", postReactionHandler)
	e.GET("/api/livestream/:livestream_id/reaction", getReactionsHandler)

	// (配信者向け)ライブコメントの報告一覧取得API
	e.GET("/api/livestream/:livestream_id/report", getLivecommentReportsHandler)
	e.GET("/api/livestream/:livestream_id/ngwords", getNgwords)
	// ライブコメント報告
	e.POST("/api/livestream/:livestream_id/livecomment/:livecomment_id/report", reportLivecommentHandler)
	// 配信者によるモデレーション (NGワード登録)
	e.POST("/api/livestream/:livestream_id/moderate", moderateHandler)

	// livestream_viewersにINSERTするため必要
	// ユーザ視聴開始 (viewer)
	e.POST("/api/livestream/:livestream_id/enter", enterLivestreamHandler)
	// ユーザ視聴終了 (viewer)
	e.DELETE("/api/livestream/:livestream_id/exit", exitLivestreamHandler)

	// user
	e.POST("/api/register", registerHandler)
	e.POST("/api/login", loginHandler)
	e.GET("/api/user/me", getMeHandler)
	// フロントエンドで、配信予約のコラボレーターを指定する際に必要
	e.GET("/api/user/:username", getUserHandler)
	e.GET("/api/user/:username/statistics", getUserStatisticsHandler)
	e.GET("/api/user/:username/icon", getIconHandler)
	e.POST("/api/icon", postIconHandler)

	// stats
	// ライブ配信統計情報
	e.GET("/api/livestream/:livestream_id/statistics", getLivestreamStatisticsHandler)

	// 課金情報
	e.GET("/api/payment", GetPaymentResult)

	e.HTTPErrorHandler = errorResponseHandler

	// DB接続
	conn, err := connectDB(e.Logger)
	if err != nil {
		e.Logger.Errorf("failed to connect db: %v", err)
		os.Exit(1)
	}
	defer conn.Close()
	dbConn = conn

	subdomainAddr, ok := os.LookupEnv(powerDNSSubdomainAddressEnvKey)
	if !ok {
		e.Logger.Errorf("environ %s must be provided", powerDNSSubdomainAddressEnvKey)
		os.Exit(1)
	}
	powerDNSSubdomainAddress = subdomainAddr

	// pprof、最後には消すこと
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	// HTTPサーバ起動
	listenAddr := net.JoinHostPort("", strconv.Itoa(listenPort))
	if err := e.Start(listenAddr); err != nil {
		e.Logger.Errorf("failed to start HTTP server: %v", err)
		os.Exit(1)
	}
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func errorResponseHandler(err error, c echo.Context) {
	c.Logger().Errorf("error at %s: %+v", c.Path(), err)
	if he, ok := err.(*echo.HTTPError); ok {
		if e := c.JSON(he.Code, &ErrorResponse{Error: err.Error()}); e != nil {
			c.Logger().Errorf("%+v", e)
		}
		return
	}

	if e := c.JSON(http.StatusInternalServerError, &ErrorResponse{Error: err.Error()}); e != nil {
		c.Logger().Errorf("%+v", e)
	}
}
