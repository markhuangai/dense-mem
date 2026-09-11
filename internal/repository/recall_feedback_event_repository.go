package repository

import (
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

type RecallFeedbackEventRepository = recallpostgres.FeedbackRepository
type RecallFeedbackEventRepositoryImpl = recallpostgres.FeedbackStore

var ErrRecallFeedbackEventNotFound = recallpostgres.ErrRecallFeedbackEventNotFound

func NewRecallFeedbackEventRepository(db *gorm.DB, rls postgres.RLSHelper) *RecallFeedbackEventRepositoryImpl {
	return recallpostgres.NewFeedbackStore(db, rls)
}
