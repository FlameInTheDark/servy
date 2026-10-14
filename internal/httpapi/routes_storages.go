package httpapi

import (
	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/backups"
)

// mountBackupStorages exposes the storage destinations backups can be uploaded
// to. Listing is available to any signed-in user (the server backup tab needs it
// for the destination picker), changing them is a panel-level operation.
func (a *App) mountBackupStorages(api fiber.Router) {
	g := api.Group("/panel/backup-storages")
	g.Get("", a.listBackupStorages)
	g.Get("/kinds", a.listBackupStorageKinds)
	g.Post("/test", a.testBackupStorageDraft)
	g.Get("/:storage_id", a.getBackupStorage)
	g.Post("", a.createBackupStorage)
	g.Patch("/:storage_id", a.patchBackupStorage)
	g.Delete("/:storage_id", a.deleteBackupStorage)
	g.Post("/:storage_id/test", a.testBackupStorage)
}

func (a *App) listBackupStorages(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	return ok(c, a.Backups.ListStorages())
}

// listBackupStorageKinds returns the backends the picker offers together with
// the fields each one needs.
func (a *App) listBackupStorageKinds(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	return ok(c, backups.AdvertisedKinds())
}

func (a *App) getBackupStorage(c fiber.Ctx) error {
	if a.requireAuth(c) == nil {
		return nil
	}
	view, err := a.Backups.GetStorageView(c.Params("storage_id"))
	if err != nil {
		return fail(c, 404, "STORAGE_NOT_FOUND")
	}
	return ok(c, view)
}

func (a *App) createBackupStorage(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var req backups.StorageInput
	if err := parseBody(c, &req); err != nil {
		return err
	}
	view, err := a.Backups.CreateStorage(req)
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, view)
}

func (a *App) patchBackupStorage(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var req backups.StorageInput
	if err := parseBody(c, &req); err != nil {
		return err
	}
	view, err := a.Backups.UpdateStorage(c.Params("storage_id"), req)
	if err != nil {
		if err == backups.ErrNotFound {
			return fail(c, 404, "STORAGE_NOT_FOUND")
		}
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, view)
}

func (a *App) deleteBackupStorage(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	force := c.Query("force") == "true"
	if err := a.Backups.DeleteStorage(c.Params("storage_id"), force); err != nil {
		switch err {
		case backups.ErrNotFound:
			return fail(c, 404, "STORAGE_NOT_FOUND")
		case backups.ErrStorageInUse:
			return fail(c, 409, "STORAGE_IN_USE")
		}
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"deleted": c.Params("storage_id")})
}

// testBackupStorage checks a stored destination.
func (a *App) testBackupStorage(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var req backups.StorageInput
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if err := a.Backups.TestStorage(c.Params("storage_id"), req); err != nil {
		return failMsg(c, 400, "STORAGE_TEST_FAILED", err.Error())
	}
	return ok(c, fiber.Map{"ok": true})
}

// testBackupStorageDraft checks a destination that has not been saved yet, so
// the form can validate credentials before storing them.
func (a *App) testBackupStorageDraft(c fiber.Ctx) error {
	if a.requireSuperuser(c) == nil {
		return nil
	}
	var req backups.StorageInput
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if err := a.Backups.TestStorage("", req); err != nil {
		return failMsg(c, 400, "STORAGE_TEST_FAILED", err.Error())
	}
	return ok(c, fiber.Map{"ok": true})
}
