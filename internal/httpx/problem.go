package httpx

import "github.com/joakimcarlsson/minmux/router"

func writeProblem(c *router.Context, pd *router.ProblemDetails) {
	c.JSON(pd.Status, pd)
}
