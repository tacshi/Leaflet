package core

import (
	"log"
	"math"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/store"
	"leaflet/locales"
)

// binSweepEvery is how often the bin deletes for good what has been in it for store.BinDays.
const binSweepEvery = time.Hour

var binNow = time.Now

// sweepBin empties the bin hourly of what has been in it for store.BinDays, on a device that does not sync. One that
// syncs does so after its pulls instead (sweepPulled): what it knew before may be old, as of a page another device has
// restored since, which it would otherwise delete for good on every device.
func (c *Core) sweepBin() {
	c.binMu.Lock()
	if c.binStop != nil {
		c.binMu.Unlock()
		return
	}
	stop := make(chan struct{})
	c.binStop = stop
	c.binMu.Unlock()
	guard.Go(func() {
		tick := time.NewTicker(binSweepEvery)
		defer tick.Stop()
		c.sweepUnsynced(binNow())
		for {
			select {
			case <-stop:
				return
			case now := <-tick.C:
				c.sweepUnsynced(now)
			}
		}
	})
}

func (c *Core) sweepUnsynced(now time.Time) {
	if !c.Sync.Syncs() {
		c.SweepBin(now)
	}
}

// sweepPulled empties the bin once a pull has brought in what the other devices did, at most every binSweepEvery.
func (c *Core) sweepPulled() {
	now := binNow()
	c.binMu.Lock()
	due := c.binSwept.IsZero() || now.Sub(c.binSwept) >= binSweepEvery
	if due {
		c.binSwept = now
	}
	c.binMu.Unlock()
	if due {
		c.SweepBin(now)
	}
}

func (c *Core) stopBinSweep() {
	c.binMu.Lock()
	stop := c.binStop
	c.binStop = nil
	c.binMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// purgeWipe is when deleting a page for good takes what was on it off this device: once the server takes it, on a
// device that syncs through one, which refuses it for a page another device restored since; at once otherwise.
func (c *Core) purgeWipe() store.Wipe {
	if c.Sync != nil && c.Sync.ThroughServer() {
		return store.WipeOnceTaken
	}
	return store.WipeNow
}

// SweepBin deletes for good what has been in the bin for store.BinDays by now.
func (c *Core) SweepBin(now time.Time) {
	switch gone, err := c.Store.PurgeOld(now, c.purgeWipe()); {
	case err != nil:
		log.Printf("bin: %v", err)
	case gone:
		c.Sync.KickPush()
	}
}

// RestoreFromBin takes a page, with the pages deleted with it, or a file out of the bin.
func (p Pages) RestoreFromBin(ref store.BinRef) error {
	if ref.Kind == store.BinFile {
		return p.RestoreFile(ref.ID)
	}
	ids, err := p.Store.InBin(ref.ID)
	if err != nil {
		return err
	}
	if err := p.RestorePages(ids, doc.Position{At: "end"}); err != nil {
		return err
	}
	p.kickPush()
	return nil
}

// TrashFile puts a file in the bin, keeping its contents until it goes for good.
func (p Pages) TrashFile(id string) error {
	if err := p.Store.TrashFile(id); err != nil {
		return err
	}
	p.kickPush()
	return nil
}

// RestoreFile takes a file out of the bin, to its folder, or the top level once that is gone.
func (p Pages) RestoreFile(id string) error {
	if err := p.Store.RestoreFile(id); err != nil {
		return err
	}
	p.kickPush()
	return nil
}

func (p Pages) kickPush() {
	if p.Sync != nil {
		p.Sync.KickPush()
	}
}

// PurgeBin deletes the items of the bin for good.
func (c *Core) PurgeBin(refs ...store.BinRef) error {
	if err := c.Store.Purge(c.purgeWipe(), refs...); err != nil {
		return err
	}
	c.Sync.KickPush()
	return nil
}

// EmptyBin deletes everything in the bin for good.
func (c *Core) EmptyBin() error {
	if err := c.Store.EmptyBin(c.purgeWipe()); err != nil {
		return err
	}
	c.Sync.KickPush()
	return nil
}

// DropNewPage deletes a page made here that never opened, which was never the person's: one that has gone nowhere
// yet goes with nothing left of it, and one that has goes for good, not to the bin, unless it is in someone else's
// space, where only its owner deletes for good.
func (c *Core) DropNewPage(id string) error {
	if dropped, err := c.Store.DropUnsent(id); err != nil || dropped {
		return err
	}
	if _, err := c.Store.DeletePage(id); err != nil {
		return err
	}
	p, err := c.Store.Page(id)
	if err != nil {
		return err
	}
	return c.PurgeBin(store.BinRef{Kind: store.BinPage, ID: id, DeletedAt: p.DeletedAt})
}

// BinWhen says when an item of the bin was deleted and how long it has left there, as
// "Deleted Yesterday · 29 days left".
func BinWhen(it store.BinItem, now time.Time) string {
	days := max(int(math.Ceil(it.GoesAt.Sub(now).Hours()/24)), 0)
	return locales.BinDeletedOn(locales.Day(it.DeletedAt, now)) + " · " + locales.BinDaysLeft(days)
}
