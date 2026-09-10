package repository

// This compatibility facade keeps the historical deletion extension name
// while Privacy owns the transaction and Access owns only its adapter call.

import privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"

type CredentialDeletionAuditInput = privacycontract.CredentialDeletionAuditInput
