package lib

import (
	"log"

	"github.com/bwmarrin/discordgo"
)

type SessionManager interface {
	InitializeSession(token string) *discordgo.Session
}

type DiscordSessionManager struct{}

func (d *DiscordSessionManager) InitializeSession(token string) *discordgo.Session {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("Discordセッションの作成に失敗: %v", err)
	}
	return dg
}
