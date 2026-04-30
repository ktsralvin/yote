package main

import (
	"encoding/json"
	"log"
	"net/http"
        "io"
	"os"
	"path/filepath"
        "github.com/google/uuid"
        "database/sql"
        _ "github.com/lib/pq"
        "strings"

)

var db *sql.DB

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type CreateArticleRequest struct {
    Title   string `json:"title"`
    Content string `json:"content"`
    MediaIDs []int `json:"media_ids"`
}

type ArticleResponse struct {
    ID      int      `json:"id"`
    Title   string   `json:"title"`
    Content string   `json:"content"`
    Media   []string `json:"media"`
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Email == "admin@yote.com" && req.Password == "123" {
		json.NewEncoder(w).Encode(map[string]string{
			"message": "Login success",
		})
		return
	}

	http.Error(w, "Invalid credentials", http.StatusUnauthorized)
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
        //CORS
        w.Header().Set("Access-Control-Allow-Origin", "*")
        w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
        w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

        // response type
        w.Header().Set("Content-Type", "application/json")

        // handle preflight request
        if r.Method == http.MethodOptions {
                return
        }

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// max upload 10MB
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		http.Error(w, "File too large", http.StatusBadRequest)
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Invalid file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// create unique name
	// filename := fmt.Sprintf("%d_%s", time.Now().Unix(), handler.Filename)
        ext := filepath.Ext(handler.Filename)
        filename := uuid.New().String() + ext

	dst, err := os.Create("/home/alvin/yote/uploads/" + filename)
	if err != nil {
		http.Error(w, "Cannot save file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	_, err = io.Copy(dst, file)
	if err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

        url := "http://192.168.56.105:8080/uploads/" + filename

        var mediaID int

        err = db.QueryRow(`
            INSERT INTO media (file_url, file_type, mime_type)
            VALUES ($1, $2, $3)
            RETURNING id
        `, url, ext, handler.Header.Get("Content-Type")).Scan(&mediaID)

        if err != nil {
            http.Error(w, "Failed to save to DB", http.StatusInternalServerError)
            return
        }

	json.NewEncoder(w).Encode(map[string]interface{}{
             "url": url,
             "id":  mediaID,
        })
}

func createArticleHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    var req CreateArticleRequest
    err := json.NewDecoder(r.Body).Decode(&req)
    if err != nil {
        http.Error(w, "Invalid request", http.StatusBadRequest)
        return
    }

    tx, err := db.Begin()
    if err != nil {
        http.Error(w, "Failed to start transaction", http.StatusInternalServerError)
        return
    }

    defer tx.Rollback() // otomatis rollback kalau belum commit

    var articleID int

    slug := strings.ToLower(req.Title)
    slug = strings.ReplaceAll(slug, " ", "-")

    // INSERT ARTICLE
    err = tx.QueryRow(`
        INSERT INTO articles (title, content, slug, author_id)
        VALUES ($1, $2, $3, $4)
        RETURNING id
    `, req.Title, req.Content, slug, 1).Scan(&articleID)

    if err != nil {
        log.Println("ARTICLE ERROR:", err)
        http.Error(w, "Failed to create article", http.StatusInternalServerError)
        return
    }

    // VALIDASI MEDIA
    for _, mediaID := range req.MediaIDs {
        var exists int

        err = tx.QueryRow("SELECT 1 FROM media WHERE id = $1", mediaID).Scan(&exists)
        if err == sql.ErrNoRows {
            http.Error(w, "Media ID not found", http.StatusBadRequest)
            return
        } else if err != nil {
            http.Error(w, "DB error", http.StatusInternalServerError)
            return
        }
    }

    // INSERT RELATION
    for _, mediaID := range req.MediaIDs {
        _, err = tx.Exec(`
            INSERT INTO article_media (article_id, media_id)
            VALUES ($1, $2)
        `, articleID, mediaID)

        if err != nil {
            log.Println("ARTICLE_MEDIA ERROR:", err)
            http.Error(w, "Failed to link media", http.StatusInternalServerError)
            return
        }
    }

    // COMMIT
    err = tx.Commit()
    if err != nil {
        http.Error(w, "Commit failed", http.StatusInternalServerError)
        return
    }

    json.NewEncoder(w).Encode(map[string]interface{}{
        "message": "Article created",
        "id":      articleID,
    })
}

func getArticlesHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    rows, err := db.Query(`
        SELECT 
            a.id,
            a.title,
            a.content,
            m.file_url
        FROM articles a
        LEFT JOIN article_media am ON am.article_id = a.id
        LEFT JOIN media m ON m.id = am.media_id
        ORDER BY a.id
    `)
    if err != nil {
        http.Error(w, "Failed to fetch articles", http.StatusInternalServerError)
        return
    }
    defer rows.Close()

    articlesMap := make(map[int]*ArticleResponse)

    for rows.Next() {
        var id int
        var title, content string
        var fileURL sql.NullString

        err := rows.Scan(&id, &title, &content, &fileURL)
        if err != nil {
            http.Error(w, "Scan error", http.StatusInternalServerError)
            return
        }

        // kalau article belum ada di map → create
        if _, exists := articlesMap[id]; !exists {
            articlesMap[id] = &ArticleResponse{
                ID:      id,
                Title:   title,
                Content: content,
                Media:   []string{},
            }
        }

        // if media exist → append
        if fileURL.Valid {
            articlesMap[id].Media = append(articlesMap[id].Media, fileURL.String)
        }
    }

    // convert map → slice
    var result []ArticleResponse
    for _, v := range articlesMap {
        result = append(result, *v)
    }

    json.NewEncoder(w).Encode(result)
}

func main() {
        var err error

        db, err = sql.Open("postgres", "postgres://yote_user:yote@localhost:5432/yote_db?sslmode=disable")
        if err != nil {
            log.Fatal(err)
        }

        err = db.Ping()
        if err != nil {
            log.Fatal("DB connection failed:", err)
        }

        http.HandleFunc("/login", loginHandler)
        http.HandleFunc("/upload", uploadHandler)

        http.HandleFunc("/articles", func(w http.ResponseWriter, r *http.Request) {
            if r.Method == http.MethodPost {
                createArticleHandler(w, r)
            } else if r.Method == http.MethodGet {
                getArticlesHandler(w, r)
            } else {
                http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
            }
        })

        http.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir("/home/alvin/yote/uploads"))))

	log.Println("Server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
