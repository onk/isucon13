package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type ReactionModel struct {
	ID           int64  `db:"id"`
	EmojiName    string `db:"emoji_name"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	CreatedAt    int64  `db:"created_at"`
}

type Reaction struct {
	ID         int64      `json:"id"`
	EmojiName  string     `json:"emoji_name"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	CreatedAt  int64      `json:"created_at"`
}

type PostReactionRequest struct {
	EmojiName string `json:"emoji_name"`
}

func getReactionsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// FIXME: index
	query := "SELECT * FROM reactions WHERE livestream_id = ? ORDER BY created_at DESC"
	if c.QueryParam("limit") != "" {
		limit, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
		}
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	reactionModels := []ReactionModel{}
	if err := tx.SelectContext(ctx, &reactionModels, query, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "failed to get reactions")
	}

	livestreamModel := LivestreamModel{}
	if err := tx.GetContext(ctx, &livestreamModel, "SELECT * FROM livestreams WHERE id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed get livestream: "+err.Error())
	}
	livestream, err := fillLivestreamResponse(ctx, tx, livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	reactions := make([]Reaction, len(reactionModels))
	user_id_list := make([]int64, len(reactionModels))
	for i := range reactionModels {
		user_id_list[i] = reactionModels[i].UserID
	}

	userList := make(map[int64]UserModel)
	query, args, err := sqlx.In("SELECT * FROM users WHERE id IN (?)", user_id_list)
	if err != nil {
		log.Fatalln(err)
	}
	query = tx.Rebind(query)

	if err := tx.SelectContext(ctx, &userList, query, args...); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed get user list: "+err.Error())
	}

	for i := range reactionModels {
		// FIXME: 5N+1
		reaction, err := fillReactionResponseLivestreamModel(ctx, tx, reactionModels[i], livestream, userList[reactionModels[i].UserID])
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction: "+err.Error())
		}

		reactions[i] = reaction
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, reactions)
}

func postReactionHandler(c echo.Context) error {
	ctx := c.Request().Context()
	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *PostReactionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	reactionModel := ReactionModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		EmojiName:    req.EmojiName,
		CreatedAt:    time.Now().Unix(),
	}

	result, err := tx.NamedExecContext(ctx, "INSERT INTO reactions (user_id, livestream_id, emoji_name, created_at) VALUES (:user_id, :livestream_id, :emoji_name, :created_at)", reactionModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert reaction: "+err.Error())
	}

	reactionID, err := result.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted reaction id: "+err.Error())
	}
	reactionModel.ID = reactionID

	reaction, err := fillReactionResponse(ctx, tx, reactionModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	err = redisClient.ZIncrBy(ctx, LivestreamLeaderBoardRedisKey, 1, c.Param("livestream_id")).Err()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to incr the leader board: "+err.Error())
	}

	livestreamUserIDStr, err := redisClient.Get(ctx, fmt.Sprintf("%s%d", livestreamID2UserIDCachePrefix, livestreamID)).Result()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to incr the leader board: "+err.Error())
	}
	livestreamUserID, _ := strconv.ParseInt(livestreamUserIDStr, 10, 64)
	err = redisClient.ZIncrBy(ctx, UserLeaderBoardRedisKey, 1, strconv.FormatInt(livestreamUserID, 10)).Err()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to incr the leader board: "+err.Error())
	}

	err = redisClient.Incr(ctx, fmt.Sprintf("%s%d", livestreamReactionsCachePrefix, livestreamID)).Err()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to incr the num of livestream reactions: "+err.Error())
	}

	err = redisClient.Incr(ctx, fmt.Sprintf("%s%d", userReactionsCachePrefix, livestreamUserID)).Err()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to incr the num of user reactions: "+err.Error())
	}

	return c.JSON(http.StatusCreated, reaction)
}

// FIXME: user情報に応じて個別に2つクエリを発行しておる。JOINにして1發で引いたほうがよくない？
// FIXME: 中でfillLivestreamResponseも呼んでる (これは中で3クエリ叩く) があり絶望
func fillReactionResponse(ctx context.Context, tx *sqlx.Tx, reactionModel ReactionModel) (Reaction, error) {
	userModel := UserModel{}
	if err := tx.GetContext(ctx, &userModel, "SELECT * FROM users WHERE id = ?", reactionModel.UserID); err != nil {
		return Reaction{}, err
	}
	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		return Reaction{}, err
	}

	livestreamModel := LivestreamModel{}
	if err := tx.GetContext(ctx, &livestreamModel, "SELECT * FROM livestreams WHERE id = ?", reactionModel.LivestreamID); err != nil {
		return Reaction{}, err
	}
	livestream, err := fillLivestreamResponse(ctx, tx, livestreamModel)
	if err != nil {
		return Reaction{}, err
	}

	reaction := Reaction{
		ID:         reactionModel.ID,
		EmojiName:  reactionModel.EmojiName,
		User:       user,
		Livestream: livestream,
		CreatedAt:  reactionModel.CreatedAt,
	}

	return reaction, nil
}

func fillReactionResponseLivestreamModel(ctx context.Context, tx *sqlx.Tx, reactionModel ReactionModel, livestream Livestream, userModel UserModel) (Reaction, error) {
	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		return Reaction{}, err
	}

	reaction := Reaction{
		ID:         reactionModel.ID,
		EmojiName:  reactionModel.EmojiName,
		User:       user,
		Livestream: livestream,
		CreatedAt:  reactionModel.CreatedAt,
	}

	return reaction, nil
}
