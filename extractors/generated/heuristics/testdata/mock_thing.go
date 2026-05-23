package mockthing

// This file's name matches mock_*.go but has no sentinel header —
// a hand-written file that happens to use the mock_ prefix should
// trigger UnverifiedGeneratedArtifact, not GeneratedArtifact.
type HandRolledMock struct{}
